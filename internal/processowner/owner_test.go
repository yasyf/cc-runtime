package processowner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/daemonkit"
	"github.com/yasyf/daemonkit/durable"
)

func scopeRecords(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	var records []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), scopeSuffix+lockSuffix) {
			records = append(records, entry.Name())
		}
	}
	return records
}

func TestIsolatedScopesRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenIsolated(t.Context(), dir, "tui")
	if err != nil {
		t.Fatalf("first OpenIsolated: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(context.Background(), first); err != nil {
			t.Errorf("close first: %v", err)
		}
	})
	second, err := OpenIsolated(t.Context(), dir, "tui")
	if err != nil {
		t.Fatalf("second OpenIsolated: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(context.Background(), second); err != nil {
			t.Errorf("close second: %v", err)
		}
	})
	if records := scopeRecords(t, filepath.Join(dir, "tui")); len(records) != 2 {
		t.Fatalf("registry records = %v, want two distinct scopes", records)
	}
}

func TestIsolatedScopeReclaimsAbandonedPeer(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "tui")

	abandoned, err := OpenIsolated(t.Context(), dir, "tui")
	if err != nil {
		t.Fatalf("OpenIsolated: %v", err)
	}
	if err := Close(context.Background(), abandoned); err != nil {
		t.Fatalf("close abandoned: %v", err)
	}
	if records := scopeRecords(t, root); len(records) != 1 {
		t.Fatalf("registry records = %v, want the abandoned scope left behind", records)
	}

	owned, err := OpenIsolated(t.Context(), dir, "tui")
	if err != nil {
		t.Fatalf("OpenIsolated after abandonment: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(context.Background(), owned); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if records := scopeRecords(t, root); len(records) != 1 {
		t.Fatalf("registry records = %v, want only this scope's", records)
	}
}

func TestOpenExcludesASecondScopeOnOneRecord(t *testing.T) {
	dir := t.TempDir()
	owned, err := Open(t.Context(), dir, "workers.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(context.Background(), owned); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	contendCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := Open(contendCtx, dir, "workers.db"); !errors.Is(err, durable.ErrLockBusy) {
		t.Fatalf("second Open = %v, want durable.ErrLockBusy", err)
	}
}

func TestOpenAdmitsCommandsImmediately(t *testing.T) {
	owned, err := Open(t.Context(), t.TempDir(), "workers.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(context.Background(), owned); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	runCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	result, err := owned.Run(runCtx, daemonkit.Cmd{
		Path: "/bin/sh", Dir: "/bin", Args: []string{"-c", "printf ready"},
		Exec: daemonkit.ServingSameUser(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(result.Stdout) != "ready" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}
