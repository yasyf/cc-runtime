package mesh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/synckit/hostregistry"
)

const testTarget = "bob@srv.tail.ts.net"

// isolateHome points HOME (and clears XDG_CONFIG_HOME) at a fresh temp dir so
// AddHost writes an isolated shared state.json.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	var err error
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hrCmdContains reports whether a hostregistry mock recorded an ssh remote
// command containing want.
func hrCmdContains(m *hostregistry.MockRunner, want string) bool {
	for _, c := range m.SSHCmdsAll() {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

func TestAddHostDetectsSelfAndCrossRegisters(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnLocal("tailscale status --json", `{"BackendState":"Running","Self":{"DNSName":"mac.tail.ts.net."}}`, nil).
		OnLocal("id -un", "alice\n", nil).
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("command -v synckitd", "/opt/homebrew/bin/synckitd\n", nil).
		OnSSH("host add", "", nil).
		DefaultSSH("", nil)

	if err := AddHost(context.Background(), r, testTarget, "", false, nil); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	g, _ := Config.Load()
	if g.Self != testSelf {
		t.Fatalf("Self = %q, want %q", g.Self, testSelf)
	}
	if !slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("Hosts = %v, want it to contain %q", g.Hosts, testTarget)
	}
	if !hrCmdContains(r, "host add "+hostregistry.ShellQuote(testSelf)+" --no-recurse") {
		t.Fatalf("inverse registration cmd not issued; calls = %v", r.SSHCmdsAll())
	}
}

// TestAddHostTailscaleStoppedAborts guards the gap synckit's DetectSelf leaves
// open: a Stopped backend can still report a stale DNS name, and persisting that
// self identity would cross-register an unusable target.
func TestAddHostTailscaleStoppedAborts(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnLocal("tailscale status --json", `{"BackendState":"Stopped","Self":{"DNSName":"mac.tail.ts.net."}}`, nil).
		OnSSH("true", "", nil)

	err := AddHost(context.Background(), r, testTarget, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "Stopped") {
		t.Fatalf("AddHost err = %v, want it to name the Stopped backend state", err)
	}
	if len(r.SSHCmdsAll()) != 0 {
		t.Fatalf("aborted before self resolution but still touched the peer: %v", r.SSHCmdsAll())
	}
	g, _ := Config.Load()
	if len(g.Hosts) != 0 || g.Self != "" {
		t.Fatalf("a Stopped tailscale still mutated the registry: self=%q hosts=%v", g.Self, g.Hosts)
	}
}

func TestAddHostExplicitSelfSkipsDetection(t *testing.T) {
	isolateHome(t)
	// No OnLocal scripted: any DetectSelf call would error unscripted.
	r := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("command -v synckitd", "/opt/homebrew/bin/synckitd\n", nil).
		OnSSH("host add", "", nil).
		DefaultSSH("", nil)

	if err := AddHost(context.Background(), r, testTarget, testSelf, false, nil); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	g, _ := Config.Load()
	if g.Self != testSelf {
		t.Fatalf("Self = %q, want %q", g.Self, testSelf)
	}
}

func TestAddHostReachabilityFailAborts(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().OnSSH("true", "", errors.New("ssh: connect to host srv port 22: Connection refused"))

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("AddHost err = %v, want a not-reachable error", err)
	}
	g, _ := Config.Load()
	if len(g.Hosts) != 0 {
		t.Fatalf("an unreachable host was persisted: %v", g.Hosts)
	}
	if hrCmdContains(r, "host add") {
		t.Fatal("inverse registration ran despite unreachable host")
	}
}

func TestAddHostBinaryMissingAborts(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "", errors.New("exit status 1")).
		OnSSH("true", "", nil)

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("AddHost err = %v, want a not-installed error", err)
	}
	g, _ := Config.Load()
	if len(g.Hosts) != 0 {
		t.Fatalf("a binary-less host was persisted: %v", g.Hosts)
	}
}

func TestAddHostNoRecurseSkipsInverse(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("command -v synckitd", "/opt/homebrew/bin/synckitd\n", nil).
		DefaultSSH("", nil)

	if err := AddHost(context.Background(), r, testTarget, testSelf, true, nil); err != nil {
		t.Fatalf("AddHost --no-recurse: %v", err)
	}
	g, _ := Config.Load()
	if !slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("host not persisted under --no-recurse: %v", g.Hosts)
	}
	if hrCmdContains(r, "host add") {
		t.Fatalf("--no-recurse still issued an inverse registration: %v", r.SSHCmdsAll())
	}
}

func TestAddHostMissingSelfWithoutTailscaleAborts(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnLocal("tailscale status --json", "", errors.New("executable file not found in $PATH")).
		OnSSH("true", "", nil)

	err := AddHost(context.Background(), r, testTarget, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "--self") {
		t.Fatalf("AddHost err = %v, want a detect-self error pointing at --self", err)
	}
	if len(r.SSHCmdsAll()) != 0 {
		t.Fatalf("aborted before self resolution but still touched the peer: %v", r.SSHCmdsAll())
	}
}
