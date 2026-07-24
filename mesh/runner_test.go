package mesh

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/worker"
)

func TestExecRunnerLocalUsesDurableTaskOwner(t *testing.T) {
	pool := testWorkerPool(t)
	out, err := NewExecRunner(pool).Local(context.Background(), []byte("request\n"), "/bin/sh", "-c", "cat")
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if out != "request\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestExecRunnerLocalPreservesOutputAndTypedFailure(t *testing.T) {
	pool := testWorkerPool(t)
	out, err := NewExecRunner(pool).Local(
		context.Background(), []byte("partial"), "/bin/sh", "-c", "cat; printf 'remote failed\\n' >&2; exit 23",
	)
	var exit *worker.ExitError
	if out != "partial" || !errors.As(err, &exit) || exit.ExitCode != 23 || !strings.Contains(err.Error(), "remote failed") {
		t.Fatalf("Local = (%q, %v)", out, err)
	}
}

func testWorkerPool(t *testing.T) *worker.Pool {
	t.Helper()
	pool, err := worker.NewPool(worker.Config{
		Capacity: 2, QueueCapacity: 2, MaxTotalRun: runnerCommandTimeout,
		MaxStdinBytes: 1 << 20, MaxStdoutBytes: 1 << 20, MaxStderrBytes: 1 << 20,
	}, &proc.Reaper{
		Store: &proc.FileStore{Path: filepath.Join(t.TempDir(), "workers.db")}, Generation: proc.OwnerGeneration{1},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	claim, err := pool.ClaimRuntime()
	if err != nil {
		t.Fatalf("ClaimRuntime: %v", err)
	}
	if err := claim.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := claim.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := claim.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return claim.Product()
}
