package mesh

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/daemonkit"
)

func TestExecRunnerLocalUsesDurableTaskOwner(t *testing.T) {
	out, err := NewExecRunner(testOwned(t)).Local(context.Background(), []byte("request\n"), "/bin/sh", "-c", "cat")
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if out != "request\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestExecRunnerLocalPreservesOutputAndTypedFailure(t *testing.T) {
	out, err := NewExecRunner(testOwned(t)).Local(
		context.Background(), []byte("partial"), "/bin/sh", "-c", "cat; printf 'remote failed\\n' >&2; exit 23",
	)
	var exit *daemonkit.ExitError
	if out != "partial" || !errors.As(err, &exit) || exit.Exit.Code != 23 || !strings.Contains(err.Error(), "remote failed") {
		t.Fatalf("Local = (%q, %v)", out, err)
	}
}

func testOwned(t *testing.T) *daemonkit.Owned {
	t.Helper()
	openCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	owned, err := daemonkit.OwnProcesses(openCtx, filepath.Join(t.TempDir(), "processes.db"))
	if err != nil {
		t.Fatalf("OwnProcesses: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := owned.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return owned
}
