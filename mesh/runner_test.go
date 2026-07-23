package mesh

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
)

type recordingTaskRunner struct {
	task   supervise.Task
	stdin  string
	stdout string
	stderr string
	err    error
}

func (r *recordingTaskRunner) Run(_ context.Context, task supervise.Task) error {
	r.task = task
	if task.Stdin != nil {
		payload, err := io.ReadAll(task.Stdin)
		if err != nil {
			return err
		}
		r.stdin = string(payload)
		_ = task.Stdin.Close()
	}
	if task.Stdout != nil {
		_, _ = io.WriteString(task.Stdout, r.stdout)
	}
	if task.Stderr != nil {
		_, _ = io.WriteString(task.Stderr, r.stderr)
	}
	return r.err
}

func TestExecRunnerLocalUsesDurableTaskOwner(t *testing.T) {
	owner := &recordingTaskRunner{stdout: "reply\n"}
	out, err := NewExecRunner(owner).Local(context.Background(), []byte("request\n"), "cc-runtime", "rpc", "status")
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if out != "reply\n" {
		t.Fatalf("stdout = %q", out)
	}
	if owner.task.RecoveryClass != proc.RecoveryTask || owner.task.Path != "cc-runtime" ||
		!slices.Equal(owner.task.Args, []string{"rpc", "status"}) || owner.stdin != "request\n" {
		t.Fatalf("task = %+v stdin=%q", owner.task, owner.stdin)
	}
}

func TestExecRunnerSSHOncePreservesSingleAttemptAndFailure(t *testing.T) {
	sentinel := errors.New("exit 23")
	owner := &recordingTaskRunner{stdout: "partial", stderr: "remote failed\n", err: sentinel}
	out, err := NewExecRunner(owner).SSHOnce(
		context.Background(), "alice@peer.tail.ts.net", "cc-runtime rpc interaction.notify --json -", []byte("payload"),
	)
	if out != "partial" || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "remote failed") {
		t.Fatalf("SSHOnce = (%q, %v)", out, err)
	}
	if owner.task.Path != "ssh" || owner.stdin != "payload" {
		t.Fatalf("task = %+v stdin=%q", owner.task, owner.stdin)
	}
	joined := strings.Join(owner.task.Args, " ")
	if !strings.Contains(joined, "alice@peer.tail.ts.net") || !strings.Contains(joined, "interaction.notify") {
		t.Fatalf("ssh args = %q", joined)
	}
}
