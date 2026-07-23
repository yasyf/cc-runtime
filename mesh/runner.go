package mesh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
	"github.com/yasyf/synckit/hostregistry"
)

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
func NewExecRunner(runner supervise.TaskRunner) Runner { return execRunner{runner: runner} }

type execRunner struct{ runner supervise.TaskRunner }

func (r execRunner) Local(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	return r.run(ctx, stdin, name, args, name+" "+strings.Join(args, " "))
}

func (r execRunner) SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	return hostregistry.ExecSSH(ctx, r.runner, target, remoteCmd, stdin)
}

func (r execRunner) SSHOnce(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	argv := hostregistry.SSHArgv(target, remoteCmd)
	return r.run(ctx, stdin, argv[0], argv[1:], "ssh "+target)
}

func (r execRunner) run(ctx context.Context, stdin []byte, path string, args []string, label string) (string, error) {
	input, err := runnerInput(stdin)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	err = r.runner.Run(ctx, supervise.Task{
		RecoveryClass: proc.RecoveryTask,
		Path:          path,
		Args:          args,
		Stdin:         input,
		Stdout:        &stdout,
		Stderr:        &stderr,
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return stdout.String(), fmt.Errorf("%s: %w: %s", label, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func runnerInput(payload []byte) (*os.File, error) {
	input, err := os.CreateTemp("", "cc-runtime-task-input-*")
	if err != nil {
		return nil, fmt.Errorf("create task input: %w", err)
	}
	_ = os.Remove(input.Name())
	if _, err := input.Write(payload); err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("write task input: %w", err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("rewind task input: %w", err)
	}
	return input, nil
}
