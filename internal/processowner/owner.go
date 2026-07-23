// Package processowner owns crash-recoverable external command pools.
package processowner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
)

const lockWait = 30 * time.Second

// Owner is one durable disposable-process owner.
type Owner struct {
	pool   *supervise.Pool
	reaper *proc.Reaper
	lock   *proc.FileLockHandle
}

// New creates an owner without recovering prior-generation processes.
func New(stateDir, storeName string, limit int) (*Owner, error) {
	return newOwner(stateDir, storeName, limit, nil)
}

// NewLocked exclusively owns lockName until Close.
func NewLocked(ctx context.Context, stateDir, storeName, lockName string, limit int) (*Owner, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create process directory: %w", err)
	}
	lock, err := (proc.FileLockSpec{
		Path: filepath.Join(stateDir, lockName), Mode: proc.FileLockExclusive, Deadline: lockWait,
	}).Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire process owner: %w", err)
	}
	owner, err := newOwner(stateDir, storeName, limit, lock)
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return owner, nil
}

func newOwner(stateDir, storeName string, limit int, lock *proc.FileLockHandle) (*Owner, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create process directory: %w", err)
	}
	generation, err := proc.ProcessGeneration()
	if err != nil {
		return nil, err
	}
	reaper := &proc.Reaper{
		Store: &proc.FileStore{Path: filepath.Join(stateDir, storeName)}, Generation: generation,
	}
	pool, err := supervise.NewPool(limit, reaper)
	if err != nil {
		return nil, err
	}
	return &Owner{pool: pool, reaper: reaper, lock: lock}, nil
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

// Close stops admission, reaps every task, and releases exclusive ownership.
func (o *Owner) Close(ctx context.Context) error {
	o.pool.Close()
	o.pool.Cancel()
	waitErr := o.pool.Wait(context.WithoutCancel(ctx))
	if o.lock == nil {
		return waitErr
	}
	return errors.Join(waitErr, o.lock.Close())
}
