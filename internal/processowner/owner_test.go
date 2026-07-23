package processowner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockedOwnerExcludesConcurrentGeneration(t *testing.T) {
	dir := t.TempDir()
	owner, err := NewLocked(context.Background(), dir, "processes.db", "processes.lock", 2)
	if err != nil {
		t.Fatalf("NewLocked: %v", err)
	}
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	_, err = NewLocked(waitCtx, dir, "processes.db", "processes.lock", 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent owner error = %v, want deadline", err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	successor, err := NewLocked(context.Background(), dir, "processes.db", "processes.lock", 2)
	if err != nil {
		t.Fatalf("successor NewLocked: %v", err)
	}
	if err := successor.Recover(context.Background()); err != nil {
		t.Fatalf("successor Recover: %v", err)
	}
	if err := successor.Close(context.Background()); err != nil {
		t.Fatalf("successor Close: %v", err)
	}
}
