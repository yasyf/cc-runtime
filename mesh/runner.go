package mesh

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yasyf/synckit/hostregistry"
)

// sshOnceWaitDelay bounds how long a single-dial ssh blocks on its pipes after
// the leader exits, mirroring synckit's own wait delay.
const sshOnceWaitDelay = 5 * time.Second

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

// NewExecRunner returns the production Runner: Local shells out directly, SSH
// rides synckit's failover dialer, SSHOnce spawns one ssh against the target.
func NewExecRunner() Runner { return execRunner{} }

type execRunner struct{}

func (execRunner) Local(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name/args are the fixed rpc passthrough, not untrusted input.
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (execRunner) SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	return hostregistry.ExecSSH(ctx, target, remoteCmd, stdin)
}

func (execRunner) SSHOnce(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	argv := hostregistry.SSHArgv(target, remoteCmd)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: argv is ssh against a registered target running the fixed rpc passthrough.
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.WaitDelay = sshOnceWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
