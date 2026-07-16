package mesh

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	testTarget = "bob@srv.tail.ts.net"
	testSelf   = "alice@mac.tail.ts.net"
)

// hasCall reports whether the mock recorded a Run with exactly argv.
func hasCall(m *MockRunner, argv ...string) bool {
	for _, c := range m.Calls() {
		if slices.Equal(c, argv) {
			return true
		}
	}
	return false
}

func TestAddHostDetectsSelfAndCrossRegisters(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	local := NewMockRunner().
		On("tailscale status", `{"BackendState":"Running","Self":{"DNSName":"mac.tail.ts.net."}}`, nil).
		On("id -un", "alice\n", nil)
	remote := NewMockRunner().
		On("true", "", nil).
		On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		On("host add", "", nil)

	if err := s.AddHost(context.Background(), local, remote, testTarget, "", false, nil); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	g, _ := s.Load()
	if g.Self != testSelf {
		t.Fatalf("Self = %q, want %q", g.Self, testSelf)
	}
	if !slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("Hosts = %v, want it to contain %q", g.Hosts, testTarget)
	}
	if !hasCall(remote, "cc-runtime", "host", "add", testSelf, "--no-recurse") {
		t.Fatalf("inverse registration argv not issued; calls = %v", remote.Calls())
	}
}

func TestAddHostExplicitSelfSkipsDetection(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	// A local runner with nothing scripted: any DetectSelf call would error.
	local := NewMockRunner()
	remote := NewMockRunner().
		On("true", "", nil).
		On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		On("host add", "", nil)

	if err := s.AddHost(context.Background(), local, remote, testTarget, testSelf, false, nil); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	if len(local.Calls()) != 0 {
		t.Fatalf("explicit --self still ran local detection: %v", local.Calls())
	}
	g, _ := s.Load()
	if g.Self != testSelf {
		t.Fatalf("Self = %q, want %q", g.Self, testSelf)
	}
}

func TestAddHostReachabilityFailAborts(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	remote := NewMockRunner().On("true", "", errors.New("ssh: connect to host srv port 22: Connection refused"))

	err := s.AddHost(context.Background(), NewMockRunner(), remote, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("AddHost err = %v, want a not-reachable error", err)
	}
	g, _ := s.Load()
	if len(g.Hosts) != 0 {
		t.Fatalf("an unreachable host was persisted: %v", g.Hosts)
	}
	if hasCall(remote, "cc-runtime", "host", "add", testSelf, "--no-recurse") {
		t.Fatal("inverse registration ran despite unreachable host")
	}
}

func TestAddHostBinaryMissingAborts(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	remote := NewMockRunner().
		On("true", "", nil).
		On("command -v cc-runtime", "", errors.New("exit status 1"))

	err := s.AddHost(context.Background(), NewMockRunner(), remote, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("AddHost err = %v, want a not-installed error", err)
	}
	g, _ := s.Load()
	if len(g.Hosts) != 0 {
		t.Fatalf("a binary-less host was persisted: %v", g.Hosts)
	}
}

func TestAddHostNoRecurseSkipsInverse(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	remote := NewMockRunner().
		On("true", "", nil).
		On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil)

	if err := s.AddHost(context.Background(), NewMockRunner(), remote, testTarget, testSelf, true, nil); err != nil {
		t.Fatalf("AddHost --no-recurse: %v", err)
	}
	g, _ := s.Load()
	if !slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("host not persisted under --no-recurse: %v", g.Hosts)
	}
	for _, c := range remote.Calls() {
		if slices.Contains(c, "host") {
			t.Fatalf("--no-recurse still issued an inverse registration: %v", c)
		}
	}
}

func TestAddHostMissingSelfWithoutTailscaleAborts(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	local := NewMockRunner().On("tailscale status", "", errors.New("executable file not found in $PATH"))
	remote := NewMockRunner().On("true", "", nil)

	err := s.AddHost(context.Background(), local, remote, testTarget, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "--self") {
		t.Fatalf("AddHost err = %v, want a detect-self error pointing at --self", err)
	}
	if len(remote.Calls()) != 0 {
		t.Fatalf("aborted before self resolution but still touched the peer: %v", remote.Calls())
	}
}

func TestVerify(t *testing.T) {
	t.Run("reachable-installed", func(t *testing.T) {
		remote := NewMockRunner().
			On("true", "", nil).
			On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
			On("version", "1.2.3\n", nil)
		res := Verify(context.Background(), remote, testTarget)
		if !res.Reachable || !res.Installed || res.Version != "1.2.3" {
			t.Fatalf("Verify = %+v, want reachable+installed with version 1.2.3", res)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		remote := NewMockRunner().On("true", "", errors.New("Connection refused"))
		res := Verify(context.Background(), remote, testTarget)
		if res.Reachable || res.Installed {
			t.Fatalf("Verify = %+v, want unreachable", res)
		}
	})
	t.Run("reachable-not-installed", func(t *testing.T) {
		remote := NewMockRunner().
			On("true", "", nil).
			On("command -v cc-runtime", "", errors.New("exit status 1"))
		res := Verify(context.Background(), remote, testTarget)
		if !res.Reachable || res.Installed {
			t.Fatalf("Verify = %+v, want reachable but not installed", res)
		}
	})
}

func TestVerifyAllPreservesOrder(t *testing.T) {
	hosts := []string{"a@x.ts.net", "b@y.ts.net", "c@z.ts.net"}
	dial := func(target string) Runner {
		m := NewMockRunner()
		if target == "b@y.ts.net" {
			return m.On("true", "", errors.New("down"))
		}
		return m.On("true", "", nil).
			On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
			On("version", "9.9.9\n", nil)
	}
	results := VerifyAll(context.Background(), hosts, dial)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, h := range hosts {
		if results[i].Target != h {
			t.Fatalf("results[%d].Target = %q, want %q", i, results[i].Target, h)
		}
	}
	if !results[0].Reachable || results[1].Reachable || !results[2].Reachable {
		t.Fatalf("reachability = %v/%v/%v, want true/false/true", results[0].Reachable, results[1].Reachable, results[2].Reachable)
	}
}
