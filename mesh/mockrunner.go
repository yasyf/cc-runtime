package mesh

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// MockRunner is a scripted Runner for tests, mirroring synckit's
// hostregistry.MockRunner but recording each call's stdin, since the rpc
// fan-out feeds params over stdin. Local replies key on the joined "name args";
// SSH replies key on a remote-command substring. Exported so the tui
// tests drive the same boundary.
type MockRunner struct {
	mu        sync.Mutex
	calls     []MockCall
	localOn   map[string]mockReply
	sshOn     []sshRule
	sshDef    mockReply
	hasSSHDef bool
}

// MockCall is one recorded invocation against a MockRunner.
type MockCall struct {
	Kind   string // "local" or "ssh"
	Target string // ssh target, or "" for local
	Cmd    string // ssh remote command, or "name arg arg" for local
	Stdin  string
}

type mockReply struct {
	out string
	err error
}

type sshRule struct {
	contains string
	reply    mockReply
}

// NewMockRunner returns a MockRunner with no scripted replies.
func NewMockRunner() *MockRunner {
	return &MockRunner{localOn: map[string]mockReply{}}
}

// OnLocal scripts the reply for a Local call whose joined "name args" equals key.
func (m *MockRunner) OnLocal(key, out string, err error) *MockRunner {
	m.localOn[key] = mockReply{out: out, err: err}
	return m
}

// OnSSH scripts the reply for any SSH call whose remote command
// contains the given substring; rules match in registration order.
func (m *MockRunner) OnSSH(contains, out string, err error) *MockRunner {
	m.sshOn = append(m.sshOn, sshRule{contains: contains, reply: mockReply{out: out, err: err}})
	return m
}

// DefaultSSH sets the reply for an SSH call matching no OnSSH rule.
func (m *MockRunner) DefaultSSH(out string, err error) *MockRunner {
	m.sshDef = mockReply{out: out, err: err}
	m.hasSSHDef = true
	return m
}

func (m *MockRunner) Local(_ context.Context, stdin []byte, name string, args ...string) (string, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	m.mu.Lock()
	m.calls = append(m.calls, MockCall{Kind: "local", Cmd: key, Stdin: string(stdin)})
	r, ok := m.localOn[key]
	m.mu.Unlock()
	if !ok {
		return "", errors.New("unscripted local: " + key)
	}
	return r.out, r.err
}

func (m *MockRunner) SSH(ctx context.Context, target, remoteCmd string, stdin []byte) (string, error) {
	return m.ssh(ctx, "ssh", target, remoteCmd, stdin)
}

func (m *MockRunner) ssh(_ context.Context, kind, target, remoteCmd string, stdin []byte) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, MockCall{Kind: kind, Target: target, Cmd: remoteCmd, Stdin: string(stdin)})
	m.mu.Unlock()
	for _, rule := range m.sshOn {
		if strings.Contains(remoteCmd, rule.contains) {
			return rule.reply.out, rule.reply.err
		}
	}
	if m.hasSSHDef {
		return m.sshDef.out, m.sshDef.err
	}
	return "", errors.New("unscripted ssh: " + remoteCmd)
}

// SSHCalls returns, in order, every ssh-kind call recorded against target.
func (m *MockRunner) SSHCalls(target string) []MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MockCall
	for _, c := range m.calls {
		if c.Kind != "local" && c.Target == target {
			out = append(out, c)
		}
	}
	return out
}

// SSHCmdsAll returns, in order, every ssh-kind remote command against any target.
func (m *MockRunner) SSHCmdsAll() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if c.Kind != "local" {
			out = append(out, c.Cmd)
		}
	}
	return out
}

// LocalCalls returns, in order, every local call recorded.
func (m *MockRunner) LocalCalls() []MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MockCall
	for _, c := range m.calls {
		if c.Kind == "local" {
			out = append(out, c)
		}
	}
	return out
}
