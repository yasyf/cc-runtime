package processowner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/worker"
)

func TestIsolatedOwnersRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	first, err := NewIsolated(context.Background(), dir, "tui", 2)
	if err != nil {
		t.Fatalf("first NewIsolated: %v", err)
	}
	second, err := NewIsolated(context.Background(), dir, "tui", 2)
	if err != nil {
		_ = first.Close(context.Background())
		t.Fatalf("second NewIsolated: %v", err)
	}
	if first.recordPath == second.recordPath || first.storePath == second.storePath {
		t.Fatalf("owners share state: first=%q second=%q", first.recordPath, second.recordPath)
	}
	if err := first.Recover(context.Background()); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	if err := second.Recover(context.Background()); err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestIsolatedOwnerRecoversOrphanedGeneration(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "tui")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record := ownerRecord{
		Schema: ownerSchemaV1, Generation: proc.OwnerGeneration{1},
		Identity: proc.Identity{PID: os.Getpid(), StartTime: "dead", Boot: "old", Comm: "cc-runtime"},
	}
	record.Store = record.Generation.String() + ".db"
	recordPath := filepath.Join(root, record.Generation.String()+".json")
	if err := writeRecord(root, recordPath, record); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	owner, err := NewIsolated(context.Background(), dir, "tui", 2)
	if err != nil {
		t.Fatalf("NewIsolated: %v", err)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		_ = owner.Close(context.Background())
		t.Fatalf("orphan record still exists: %v", err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOwnerCloseSettlesBeforeExplicitRecovery(t *testing.T) {
	owner, err := New(t.TempDir(), "workers.db", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOwnerCloseBoundsLifecycleAcquisition(t *testing.T) {
	owner, err := New(t.TempDir(), "workers.db", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := owner.acquire(t.Context()); err != nil {
		t.Fatalf("acquire lifecycle: %v", err)
	}
	previous := settlementTimeout
	settlementTimeout = 20 * time.Millisecond
	defer func() { settlementTimeout = previous }()
	if err := owner.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while lifecycle held = %v, want deadline exceeded", err)
	}
	settlementTimeout = previous
	owner.release()
	if err := owner.Close(context.Background()); err != nil {
		t.Fatalf("Close after lifecycle release: %v", err)
	}
}

func TestOwnerOpensAdmissionOnlyAfterRecovery(t *testing.T) {
	owner, err := New(t.TempDir(), "workers.db", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	request := worker.CommandRequest{
		Path: "/bin/sh", Dir: "/bin", Args: []string{"-c", "printf ready"}, TotalTimeout: time.Minute,
	}
	if _, err := owner.Runner().Run(t.Context(), request); !errors.Is(err, worker.ErrRuntimeOwnership) {
		t.Fatalf("run before recovery = %v", err)
	}
	if err := owner.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	result, err := owner.Runner().Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run after recovery: %v", err)
	}
	if string(result.Stdout) != "ready" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}
