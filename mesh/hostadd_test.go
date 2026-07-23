package mesh

import (
	"context"
	"errors"
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
	t.Setenv("HOME", t.TempDir())
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
		OnSSH("host add", "", nil).
		OnSSH("true", "", nil)

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
		OnSSH("host add", "", nil).
		OnSSH("true", "", nil)

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
		OnSSH("true", "", nil)

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

// TestAddHostInverseFailureRollsBack pins the rollback: a failed inverse
// registration must not leave a one-sided peering in the local registry.
func TestAddHostInverseFailureRollsBack(t *testing.T) {
	isolateHome(t)
	r := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("host add", "", errors.New("exit status 1")).
		OnSSH("true", "", nil)

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "register inverse host") {
		t.Fatalf("AddHost err = %v, want an inverse-registration error", err)
	}
	g, _ := Config.Load()
	if len(g.Hosts) != 0 || g.Self != testSelf {
		t.Fatalf("inverse failure left a one-sided peering: self=%q hosts=%v", g.Self, g.Hosts)
	}
}

// TestAddHostInverseFailureKeepsPreexistingHost asserts the rollback removes only
// the host this call added: a host registered before the failed add stays, and
// Self (the machine's own identity) stays established at the detected value.
func TestAddHostInverseFailureKeepsPreexistingHost(t *testing.T) {
	isolateHome(t)
	const priorHost = "dave@prior.tail.ts.net"
	if err := Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mesh: %v", err)
	}
	if _, err := Config.Update(context.Background(), func(g *hostregistry.Registry) error {
		g.UpsertHost(priorHost)
		return nil
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	r := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("host add", "", errors.New("exit status 1")).
		OnSSH("true", "", nil)

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "register inverse host") {
		t.Fatalf("AddHost err = %v, want an inverse-registration error", err)
	}
	g, _ := Config.Load()
	if slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("the failed add's own host survived rollback: %v", g.Hosts)
	}
	if !slices.Contains(g.Hosts, priorHost) || g.Self != testSelf {
		t.Fatalf("rollback dropped state it should keep: self=%q hosts=%v", g.Self, g.Hosts)
	}
}

// interceptingRunner delegates to a MockRunner but runs onHostAdd before the
// inverse `host add` ssh — the window where the registry lock is released and a
// concurrent AddHost can complete.
type interceptingRunner struct {
	*hostregistry.MockRunner
	onHostAdd func()
}

func (r *interceptingRunner) SSH(ctx context.Context, target, remoteCmd string) (string, error) {
	if strings.Contains(remoteCmd, "host add") {
		r.onHostAdd()
	}
	return r.MockRunner.SSH(ctx, target, remoteCmd)
}

// TestAddHostInverseFailureRollbackSparesConcurrentAdd pins the rollback's
// scope: it reverts only this call's still-standing writes, never a concurrent
// add's registration or Self committed while the lock was released for ssh.
func TestAddHostInverseFailureRollbackSparesConcurrentAdd(t *testing.T) {
	isolateHome(t)
	const otherTarget = "carol@other.tail.ts.net"
	const otherSelf = "alice@newmac.tail.ts.net"
	mock := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("host add", "", errors.New("exit status 1")).
		OnSSH("true", "", nil)
	r := &interceptingRunner{MockRunner: mock, onHostAdd: func() {
		if _, err := Config.Update(context.Background(), func(g *hostregistry.Registry) error {
			g.UpsertHost(otherTarget)
			g.Self = otherSelf
			return nil
		}); err != nil {
			t.Fatalf("concurrent update: %v", err)
		}
	}}

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "register inverse host") {
		t.Fatalf("AddHost err = %v, want an inverse-registration error", err)
	}
	g, _ := Config.Load()
	if slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("the failed add's own registration survived rollback: %v", g.Hosts)
	}
	if !slices.Contains(g.Hosts, otherTarget) {
		t.Fatalf("rollback removed the concurrent add's registration: %v", g.Hosts)
	}
	if g.Self != otherSelf {
		t.Fatalf("rollback clobbered the concurrent add's Self: %q, want %q", g.Self, otherSelf)
	}
}

// TestAddHostInverseCtxDeathStillRollsBack pins the rollback context: the
// inverse ssh failing because ctx expired must not also doom the rollback
// write, which would leave the one-sided peering the rollback exists to undo.
func TestAddHostInverseCtxDeathStillRollsBack(t *testing.T) {
	isolateHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("host add", "", context.Canceled).
		OnSSH("true", "", nil)
	r := &interceptingRunner{MockRunner: mock, onHostAdd: cancel}

	err := AddHost(ctx, r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "register inverse host") {
		t.Fatalf("AddHost err = %v, want an inverse-registration error", err)
	}
	if strings.Contains(err.Error(), "roll back") {
		t.Fatalf("AddHost err = %v, the rollback write failed on the dead ssh context", err)
	}
	g, _ := Config.Load()
	if len(g.Hosts) != 0 || g.Self != testSelf {
		t.Fatalf("rollback did not land: self=%q hosts=%v", g.Self, g.Hosts)
	}
}

// TestAddHostInverseFailureSameSelfConcurrentKeepsSelf pins the fix for the
// same-self concurrency case: two host adds on one machine detect the identical
// Self, so a failed add must not blank the Self a concurrent add established.
func TestAddHostInverseFailureSameSelfConcurrentKeepsSelf(t *testing.T) {
	isolateHome(t)
	const otherTarget = "carol@other.tail.ts.net"
	mock := hostregistry.NewMockRunner().
		OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
		OnSSH("host add", "", errors.New("exit status 1")).
		OnSSH("true", "", nil)
	r := &interceptingRunner{MockRunner: mock, onHostAdd: func() {
		if _, err := Config.Update(context.Background(), func(g *hostregistry.Registry) error {
			g.UpsertHost(otherTarget)
			g.Self = testSelf
			return nil
		}); err != nil {
			t.Fatalf("concurrent update: %v", err)
		}
	}}

	err := AddHost(context.Background(), r, testTarget, testSelf, false, nil)
	if err == nil || !strings.Contains(err.Error(), "register inverse host") {
		t.Fatalf("AddHost err = %v, want an inverse-registration error", err)
	}
	g, _ := Config.Load()
	if g.Self != testSelf {
		t.Fatalf("rollback blanked Self under same-self concurrency: %q, want %q", g.Self, testSelf)
	}
	if slices.Contains(g.Hosts, testTarget) {
		t.Fatalf("the failed add's own host survived rollback: %v", g.Hosts)
	}
	if !slices.Contains(g.Hosts, otherTarget) {
		t.Fatalf("rollback removed the concurrent add's host: %v", g.Hosts)
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
