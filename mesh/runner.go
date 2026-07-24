package mesh

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yasyf/daemonkit/worker"
	"github.com/yasyf/synckit/hostregistry"
)

const runnerCommandTimeout = 12 * time.Minute

// Runner executes the `cc-runtime rpc` passthrough for the mesh fan-out. Every
// leg feeds the JSON params over stdin — never argv, which any process on
// either machine can read from the process table. Reads (list, pending,
// presence) and answer go over SSH with synckit's multi-address failover;
// repeated answers are safe because the daemon dedupes per question. The
// non-idempotent notify leg goes over SSHOnce, a single dial attempt, because
// failover re-runs the remote command at-least-once and would duplicate an
// append the peer already recorded.
type Runner interface {
	// Local runs name with args on this machine, feeding stdin, and returns stdout.
	Local(ctx context.Context, stdin []byte, name string, args ...string) (string, error)
	// SSH runs remoteCmd on target over synckit's failover dial, feeding stdin.
	SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error)
	// SSHOnce runs remoteCmd on target with exactly one dial attempt, feeding stdin.
	SSHOnce(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error)
}

// NewExecRunner returns the production Runner backed by runner's durable
// process ownership.
func NewExecRunner(runner *worker.Pool) Runner { return execRunner{runner: runner} }

type execRunner struct{ runner *worker.Pool }

func (r execRunner) Local(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	executable, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve absolute %s: %w", name, err)
	}
	return r.run(ctx, stdin, filepath.Clean(executable), args, name+" "+strings.Join(args, " "))
}

func (r execRunner) SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	return hostregistry.ExecSSH(ctx, r.runner, target, remoteCmd, stdin)
}

func (r execRunner) SSHOnce(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	return hostregistry.ExecBootstrapSSH(ctx, r.runner, target, remoteCmd, stdin)
}

func (r execRunner) run(ctx context.Context, stdin []byte, path string, args []string, label string) (string, error) {
	result, err := r.runner.Run(ctx, worker.CommandRequest{
		Path: path, Dir: filepath.Dir(path), Args: args, Stdin: stdin, TotalTimeout: runnerCommandTimeout,
	})
	if err != nil {
		return string(result.Stdout), fmt.Errorf("%s: %w: %s", label, err, strings.TrimSpace(string(result.Stderr)))
	}
	return string(result.Stdout), nil
}
