package mesh

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// sshBin is the ssh binary the SSHRunner shells.
const sshBin = "ssh"

// dialOpts are the per-attempt ssh options: BatchMode so a missing key fails
// instead of prompting, a short ConnectTimeout so a dead host fails fast, and
// keepalives so a wedged peer drops rather than hangs.
var dialOpts = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=5",
	"-o", "ServerAliveInterval=5",
	"-o", "ServerAliveCountMax=3",
}

// Runner runs a command argv, returning its stdout and stderr. A LocalRunner
// runs it on this machine; an SSHRunner runs it on its bound peer over ssh.
type Runner interface {
	Run(ctx context.Context, argv ...string) (stdout, stderr string, err error)
}

// LocalRunner runs commands on this machine.
type LocalRunner struct{}

func (LocalRunner) Run(ctx context.Context, argv ...string) (string, string, error) {
	return runArgv(ctx, argv)
}

// SSHRunner runs commands on Target over ssh.
type SSHRunner struct {
	Target string
}

func (r SSHRunner) Run(ctx context.Context, argv ...string) (string, string, error) {
	return runArgv(ctx, SSHArgv(r.Target, argv))
}

// SSHArgv is the full ssh argv that runs argv on target: the dial options, the
// target, then argv shell-quoted into one remote command string. argv[0] is
// "ssh".
func SSHArgv(target string, argv []string) []string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = ShellQuote(a)
	}
	remote := strings.Join(quoted, " ")
	return append(append([]string{sshBin}, dialOpts...), target, remote)
}

// ShellQuote single-quotes s so it survives intact as one argument to a remote
// shell, escaping any embedded single quotes.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runArgv(ctx context.Context, argv []string) (string, string, error) {
	//nolint:gosec // G204: argv comes from trusted local state (registered hosts, the fixed cc-runtime binary name), not untrusted input.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return stdout.String(), stderr.String(), nil
}
