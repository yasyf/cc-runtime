package processowner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/daemonkit/proc"
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
		Schema: ownerSchemaV1, Generation: "orphan", Store: "orphan.db",
		Identity: proc.Identity{PID: os.Getpid(), StartTime: "dead", Boot: "old", Comm: "cc-runtime"},
	}
	recordPath := filepath.Join(root, "orphan.json")
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
