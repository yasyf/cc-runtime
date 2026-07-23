// Package processowner owns crash-recoverable external command pools.
package processowner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
)

const (
	lockWait      = 30 * time.Second
	ownerSchemaV1 = 1
)

// Owner is one durable disposable-process owner.
type Owner struct {
	pool         *supervise.Pool
	reaper       *proc.Reaper
	registryRoot string
	recordPath   string
	storePath    string
}

type ownerRecord struct {
	Schema     int           `json:"schema"`
	Generation string        `json:"generation"`
	Identity   proc.Identity `json:"identity"`
	Store      string        `json:"store"`
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
	storeName := generation + ".db"
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
	recordPath := filepath.Join(root, generation+".json")
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

func newOwner(storePath, generation string, limit int) (*Owner, error) {
	reaper := &proc.Reaper{Store: &proc.FileStore{Path: storePath}, Generation: generation}
	pool, err := supervise.NewPool(limit, reaper)
	if err != nil {
		return nil, err
	}
	return &Owner{pool: pool, reaper: reaper, storePath: storePath}, nil
}

func randomGeneration() (string, error) {
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return "", fmt.Errorf("generate process owner identity: %w", err)
	}
	return hex.EncodeToString(identity[:]), nil
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

func recoverOrphans(ctx context.Context, root, generation string, limit int) error {
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
			return fmt.Errorf("probe process owner %s: %w", record.Generation, err)
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
	if record.Schema != ownerSchemaV1 || record.Generation == "" ||
		name != record.Generation+".json" || record.Store != record.Generation+".db" ||
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

// Runner returns the owner's durable task runner.
func (o *Owner) Runner() supervise.TaskRunner { return o.pool }

// Recover settles every prior-generation process and receipt.
func (o *Owner) Recover(ctx context.Context) error {
	if err := o.pool.Recover(ctx); err != nil {
		return fmt.Errorf("recover processes: %w", err)
	}
	if _, err := o.reaper.RecoverReapReceipts(ctx, proc.RecoveryTask, func(context.Context, proc.ReapReceipt) error {
		return nil
	}); err != nil {
		return fmt.Errorf("recover process receipts: %w", err)
	}
	return nil
}

// Close stops admission, reaps every task, and retires the owner record.
func (o *Owner) Close(ctx context.Context) error {
	o.pool.Close()
	o.pool.Cancel()
	waitErr := o.pool.Wait(context.WithoutCancel(ctx))
	if o.registryRoot == "" {
		return waitErr
	}
	lock, lockErr := acquireRegistry(context.WithoutCancel(ctx), o.registryRoot)
	if lockErr != nil {
		return errors.Join(waitErr, lockErr)
	}
	removeErr := removeOwnerFiles(o.registryRoot, o.recordPath, o.storePath)
	return errors.Join(waitErr, removeErr, lock.Close())
}
