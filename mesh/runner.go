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
// presence), answer, and notify go over SSH with synckit's multi-address
// failover. Repeated answers are safe because the daemon dedupes per question;
// routed notifications carry a stable delivery key the receiving daemon
// persists and dedupes.
type Runner interface {
	// Local runs name with args on this machine, feeding stdin, and returns stdout.
	Local(ctx context.Context, stdin []byte, name string, args ...string) (string, error)
	// SSH runs remoteCmd on target over synckit's failover dial, feeding stdin.
	SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error)
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

func (r execRunner) run(ctx context.Context, stdin []byte, path string, args []string, label string) (string, error) {
	result, err := r.runner.Run(ctx, worker.CommandRequest{
		Path: path, Dir: filepath.Dir(path), Args: args, Stdin: stdin, TotalTimeout: runnerCommandTimeout,
	})
	if err != nil {
		return string(result.Stdout), fmt.Errorf("%s: %w: %s", label, err, strings.TrimSpace(string(result.Stderr)))
	}
	return string(result.Stdout), nil
}
