// Package processowner owns crash-recoverable external command pools.
package processowner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/worker"
)

const (
	lockWait           = 30 * time.Second
	settlementTimeout  = 30 * time.Second
	commandTimeout     = 12 * time.Minute
	commandInputLimit  = 16 << 20
	commandOutputLimit = 16 << 20
	commandErrorLimit  = 1 << 20
	ownerSchemaV1      = 1
)

// Owner is one durable disposable-process owner.
type Owner struct {
	mu             sync.Mutex
	pool           *worker.Pool
	claim          *worker.RuntimeClaim
	reaper         *proc.Reaper
	registryRoot   string
	recordPath     string
	storePath      string
	claimRecovered bool
	ready          bool
	closed         bool
}

type ownerRecord struct {
	Schema     int                  `json:"schema"`
	Generation proc.OwnerGeneration `json:"generation"`
	Identity   proc.Identity        `json:"identity"`
	Store      string               `json:"store"`
}

// New creates an owner without recovering prior-generation processes.
func New(stateDir, storeName string, limit int) (*Owner, error) {
	generation, err := proc.ProcessGeneration()
	if err != nil {
		return nil, err
	}
	return newOwner(filepath.Join(stateDir, storeName), generation, limit)
}

// NewIsolated creates one concurrently live owner and recovers orphaned peers.
func NewIsolated(ctx context.Context, stateDir, registryName string, limit int) (*Owner, error) {
	root := filepath.Join(stateDir, registryName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create process registry: %w", err)
	}
	lock, err := acquireRegistry(ctx, root)
	if err != nil {
		return nil, err
	}

	generation, err := randomGeneration()
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	if err := recoverOrphans(ctx, root, generation, limit); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	storeName := generation.String() + ".db"
	owner, err := newOwner(filepath.Join(root, storeName), generation, limit)
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	identity, err := proc.Probe(os.Getpid())
	if err != nil {
		_ = owner.Close(ctx)
		return nil, errors.Join(fmt.Errorf("probe process owner: %w", err), lock.Close())
	}
	record := ownerRecord{
		Schema: ownerSchemaV1, Generation: generation, Identity: identity, Store: storeName,
	}
	recordPath := filepath.Join(root, generation.String()+".json")
	if err := writeRecord(root, recordPath, record); err != nil {
		_ = owner.Close(ctx)
		return nil, errors.Join(err, lock.Close())
	}
	if err := lock.Close(); err != nil {
		_ = owner.Close(ctx)
		return nil, err
	}
	owner.registryRoot = root
	owner.recordPath = recordPath
	return owner, nil
}

func newOwner(storePath string, generation proc.OwnerGeneration, limit int) (*Owner, error) {
	reaper := &proc.Reaper{Store: &proc.FileStore{Path: storePath}, Generation: generation}
	pool, err := worker.NewPool(worker.Config{
		Capacity: limit, QueueCapacity: limit, MaxTotalRun: commandTimeout,
		MaxStdinBytes: commandInputLimit, MaxStdoutBytes: commandOutputLimit, MaxStderrBytes: commandErrorLimit,
	}, reaper)
	if err != nil {
		return nil, err
	}
	claim, err := pool.ClaimRuntime()
	if err != nil {
		return nil, err
	}
	return &Owner{pool: pool, claim: claim, reaper: reaper, storePath: storePath}, nil
}

func randomGeneration() (proc.OwnerGeneration, error) {
	var generation proc.OwnerGeneration
	if _, err := rand.Read(generation[:]); err != nil {
		return proc.OwnerGeneration{}, fmt.Errorf("generate process owner identity: %w", err)
	}
	if generation == (proc.OwnerGeneration{}) {
		return proc.OwnerGeneration{}, errors.New("generate process owner identity: random source returned zero")
	}
	return generation, nil
}

func acquireRegistry(ctx context.Context, root string) (*proc.FileLockHandle, error) {
	lock, err := (proc.FileLockSpec{
		Path: filepath.Join(root, "owners.lock"), Mode: proc.FileLockExclusive, Deadline: lockWait,
	}).Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire process registry: %w", err)
	}
	return lock, nil
}

func recoverOrphans(ctx context.Context, root string, generation proc.OwnerGeneration, limit int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read process registry: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		recordPath := filepath.Join(root, entry.Name())
		record, err := readRecord(recordPath)
		if err != nil {
			return err
		}
		if err := validateRecord(entry.Name(), record); err != nil {
			return err
		}
		live, err := ownerLive(record.Identity)
		if err != nil {
			return fmt.Errorf("probe process owner %s: %w", record.Generation.String(), err)
		}
		if live {
			continue
		}
		storePath := filepath.Join(root, record.Store)
		owner, err := newOwner(storePath, generation, limit)
		if err != nil {
			return err
		}
		if err := owner.Recover(ctx); err != nil {
			_ = owner.Close(ctx)
			return err
		}
		if err := owner.Close(ctx); err != nil {
			return err
		}
		if err := removeOwnerFiles(root, recordPath, storePath); err != nil {
			return err
		}
	}
	return nil
}

func ownerLive(want proc.Identity) (bool, error) {
	got, err := proc.Probe(want.PID)
	if errors.Is(err, proc.ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got.PID == want.PID && got.StartTime == want.StartTime && got.Boot == want.Boot && got.Comm == want.Comm, nil
}

func readRecord(path string) (ownerRecord, error) {
	f, err := os.Open(path) //nolint:gosec // registry paths are rooted under private state.
	if err != nil {
		return ownerRecord{}, fmt.Errorf("open process owner record: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var record ownerRecord
	if err := decoder.Decode(&record); err != nil {
		return ownerRecord{}, fmt.Errorf("decode process owner record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ownerRecord{}, errors.New("process owner record has trailing content")
	}
	return record, nil
}

func validateRecord(name string, record ownerRecord) error {
	if record.Schema != ownerSchemaV1 || record.Generation == (proc.OwnerGeneration{}) ||
		name != record.Generation.String()+".json" || record.Store != record.Generation.String()+".db" ||
		record.Identity.PID <= 1 || record.Identity.StartTime == "" ||
		record.Identity.Boot == "" || record.Identity.Comm == "" {
		return fmt.Errorf("invalid process owner record %q", name)
	}
	return nil
}

func writeRecord(root, path string, record ownerRecord) (err error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode process owner record: %w", err)
	}
	f, err := os.CreateTemp(root, ".owner-*")
	if err != nil {
		return fmt.Errorf("create process owner record: %w", err)
	}
	tmp := f.Name()
	defer func() {
		_ = os.Remove(tmp)
		_ = f.Close()
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish process owner record: %w", err)
	}
	return syncDir(root)
}

func removeOwnerFiles(root, recordPath, storePath string) error {
	for _, path := range []string{recordPath, storePath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove process owner state: %w", err)
		}
	}
	return syncDir(root)
}

func syncDir(path string) error {
	dir, err := os.Open(path) //nolint:gosec // caller-owned state directory.
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

// Runner returns the owner's durable worker pool.
func (o *Owner) Runner() *worker.Pool { return o.pool }

// Recover settles every prior-generation process and receipt.
func (o *Owner) Recover(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recoverLocked(ctx)
}

func (o *Owner) recoverLocked(ctx context.Context) error {
	if o.closed {
		return errors.New("process owner is closed")
	}
	if o.ready {
		return nil
	}
	if !o.claimRecovered {
		if err := o.claim.Recover(ctx); err != nil {
			return fmt.Errorf("recover processes: %w", err)
		}
		o.claimRecovered = true
	}
	if _, err := o.reaper.RecoverReapReceipts(ctx, proc.RecoveryTaskID, func(context.Context, proc.ReapReceipt) error {
		return nil
	}); err != nil {
		return fmt.Errorf("recover process receipts: %w", err)
	}
	if err := o.claim.Activate(); err != nil {
		return fmt.Errorf("activate process owner: %w", err)
	}
	o.ready = true
	return nil
}

// Close stops admission, reaps every task, and retires the owner record.
func (o *Owner) Close(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settlementTimeout)
	defer cancel()
	if err := o.recoverLocked(settleCtx); err != nil {
		return err
	}
	if err := o.claim.Close(settleCtx); err != nil {
		return err
	}
	if o.registryRoot == "" {
		o.closed = true
		return nil
	}
	lock, lockErr := acquireRegistry(settleCtx, o.registryRoot)
	if lockErr != nil {
		return lockErr
	}
	removeErr := removeOwnerFiles(o.registryRoot, o.recordPath, o.storePath)
	result := errors.Join(removeErr, lock.Close())
	if result == nil {
		o.closed = true
	}
	return result
}
