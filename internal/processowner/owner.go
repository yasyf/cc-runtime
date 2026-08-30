// Package processowner owns crash-recoverable external command scopes.
package processowner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yasyf/daemonkit"
	"github.com/yasyf/daemonkit/durable"
)

const (
	// openBudget bounds the store lock and the prior-generation reclaim.
	openBudget = 30 * time.Second
	// probeBudget bounds one liveness probe. The underlying flock is attempted
	// once, non-blocking, before any wait, so an unowned scope answers
	// immediately and the budget only has to outlast scheduling noise. It is
	// deliberately small: every live peer costs one probe, and a budget that
	// waited on contention would spend the caller's whole deadline proving
	// peers are alive.
	probeBudget = 250 * time.Millisecond
	// SettleBudget bounds a scope's close: every live child terminated and
	// proven gone. Callers close under it so a cancelled parent context still
	// settles rather than abandoning children to the next generation.
	SettleBudget = 30 * time.Second
	// registryLockName serializes the scan that reclaims dead peers' scopes.
	registryLockName = "owners.lock"
	scopeSuffix      = ".db"
	// lockSuffix names the exclusive lock daemonkit takes beside a scope's
	// record. The record itself is written only once something is recorded, so
	// the lock is the one file every scope leaves behind — and, being the thing
	// a live owner holds, it is also the liveness signal the registry scans.
	lockSuffix = ".lock"
)

// Open owns the crash-recoverable process scope recorded at storeName under
// stateDir, reclaiming whatever a prior generation left behind. One scope is
// live per record at a time; a second Open on the same record refuses with
// durable.ErrLockBusy rather than reclaiming a running peer's children.
func Open(ctx context.Context, stateDir, storeName string) (*daemonkit.Owned, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create process state directory: %w", err)
	}
	return open(ctx, filepath.Join(stateDir, storeName))
}

// OpenIsolated owns one concurrently-live process scope under
// stateDir/registryName and reclaims the scopes dead peers left behind. Each
// live peer holds its own record, so several callers own processes at once —
// the shape a TUI needs, where Open's single-record exclusion would refuse the
// second window.
//
// Liveness is the record's own exclusive lock rather than a probe of a recorded
// pid: a scope whose lock can be taken has no live owner, and taking it is
// exactly what reclaims its leaked children.
func OpenIsolated(ctx context.Context, stateDir, registryName string) (*daemonkit.Owned, error) {
	root := filepath.Join(stateDir, registryName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create process registry: %w", err)
	}
	lockCtx, cancel := context.WithTimeout(ctx, openBudget)
	defer cancel()
	registry, err := durable.AcquireLock(lockCtx, filepath.Join(root, registryLockName))
	if err != nil {
		return nil, fmt.Errorf("acquire process registry: %w", err)
	}
	if err := reclaimAbandoned(ctx, root); err != nil {
		return nil, errors.Join(err, registry.Close())
	}
	name, err := scopeName()
	if err != nil {
		return nil, errors.Join(err, registry.Close())
	}
	owned, err := open(ctx, filepath.Join(root, name))
	if err != nil {
		return nil, errors.Join(err, registry.Close())
	}
	if err := registry.Close(); err != nil {
		return nil, errors.Join(err, closeScope(ctx, owned))
	}
	return owned, nil
}

func open(ctx context.Context, recordPath string) (*daemonkit.Owned, error) {
	openCtx, cancel := context.WithTimeout(ctx, openBudget)
	defer cancel()
	owned, err := daemonkit.OwnProcesses(openCtx, recordPath)
	if err != nil {
		return nil, fmt.Errorf("own processes %s: %w", recordPath, err)
	}
	return owned, nil
}

// Close settles the scope under its own budget, so a cancelled caller context
// still terminates and proves gone every child the scope started.
func Close(ctx context.Context, owned *daemonkit.Owned) error { return closeScope(ctx, owned) }

func closeScope(ctx context.Context, owned *daemonkit.Owned) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SettleBudget)
	defer cancel()
	return owned.Close(settleCtx)
}

// reclaimAbandoned settles and removes every registry scope no peer still owns.
// Reopening an abandoned record reclaims the children its owner leaked; a live
// peer's is left alone. The caller holds the registry lock, so no second
// reclaimer competes and no live peer can be holding a record this proved free.
func reclaimAbandoned(ctx context.Context, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read process registry: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), scopeSuffix+lockSuffix) {
			continue
		}
		recordPath := filepath.Join(root, strings.TrimSuffix(entry.Name(), lockSuffix))
		unowned, err := unowned(ctx, recordPath)
		if err != nil {
			return err
		}
		if !unowned {
			continue
		}
		owned, err := open(ctx, recordPath)
		if err != nil {
			return err
		}
		if err := closeScope(ctx, owned); err != nil {
			return err
		}
		for _, path := range []string{recordPath, recordPath + lockSuffix} {
			if err := durable.Remove(path); err != nil {
				return fmt.Errorf("remove reclaimed process scope %s: %w", path, err)
			}
		}
	}
	return nil
}

// unowned reports whether the scope at recordPath has no live owner, by
// taking and releasing the very lock its owner would be holding. It probes
// rather than reclaiming in place because reclaiming is unbounded — a scope with
// children to settle takes as long as they do — while a live peer must be
// recognized in the time an uncontended lock takes.
func unowned(ctx context.Context, recordPath string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	lock, err := durable.AcquireLock(probeCtx, recordPath+lockSuffix)
	if errors.Is(err, durable.ErrLockBusy) || errors.Is(err, context.DeadlineExceeded) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe process scope %s: %w", recordPath, err)
	}
	if err := lock.Close(); err != nil {
		return false, fmt.Errorf("release process scope probe %s: %w", recordPath, err)
	}
	return true, nil
}

func scopeName() (string, error) {
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return "", fmt.Errorf("generate process scope identity: %w", err)
	}
	return hex.EncodeToString(generation[:]) + scopeSuffix, nil
}
