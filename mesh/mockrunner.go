package mesh

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MockRunner is a scripted Runner for tests: it records every Run in order and
// returns canned replies keyed on a substring of the joined argv, so a test can
// script by intent (e.g. "command -v"). Exported so callers outside the package
// can drive the same boundary.
type MockRunner struct {
	mu     sync.Mutex
	calls  [][]string
	rules  []mockRule
	def    mockReply
	hasDef bool
}

type mockRule struct {
	contains string
	reply    mockReply
}

type mockReply struct {
	stdout string
	err    error
}

// NewMockRunner returns a MockRunner with no scripted replies.
func NewMockRunner() *MockRunner { return &MockRunner{} }

// On scripts the reply for any Run whose joined argv contains substring; rules
// match in registration order.
func (m *MockRunner) On(contains, stdout string, err error) *MockRunner {
	m.rules = append(m.rules, mockRule{contains: contains, reply: mockReply{stdout: stdout, err: err}})
	return m
}

// Default sets the reply for a Run matching no On rule.
func (m *MockRunner) Default(stdout string, err error) *MockRunner {
	m.def = mockReply{stdout: stdout, err: err}
	m.hasDef = true
	return m
}

func (m *MockRunner) Run(_ context.Context, argv ...string) (string, string, error) {
	joined := strings.Join(argv, " ")
	m.mu.Lock()
	m.calls = append(m.calls, append([]string(nil), argv...))
	m.mu.Unlock()
	for _, r := range m.rules {
		if strings.Contains(joined, r.contains) {
			return r.reply.stdout, "", r.reply.err
		}
	}
	if m.hasDef {
		return m.def.stdout, "", m.def.err
	}
	return "", "", fmt.Errorf("unscripted run: %s", joined)
}

// Calls returns every recorded argv in order.
func (m *MockRunner) Calls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]string, len(m.calls))
	copy(out, m.calls)
	return out
}
