package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
)

// attendedFn is a Router.Attended seam returning a fixed local-attendance verdict.
func attendedFn(attended bool) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return attended, nil }
}

// presenceReply marshals the daemon.Reply a peer's `rpc mesh.presence` prints.
func presenceReply(attended bool) string {
	body, _ := json.Marshal(Presence{Attended: attended})
	line, _ := json.Marshal(daemon.Reply{OK: true, Body: body})
	return string(line)
}

// okNotifyReply marshals the daemon.Reply a peer's `rpc interaction.notify`
// prints on success.
func okNotifyReply() string {
	line, _ := json.Marshal(daemon.Reply{OK: true, Body: json.RawMessage(`{"ok":true}`)})
	return string(line)
}

// attendedPeer scripts a peer runner that reports attended and accepts a surface.
func attendedPeer() *MockRunner {
	return NewMockRunner().OnSSH("mesh.presence", presenceReply(true), nil).OnSSH("interaction.notify", okNotifyReply(), nil)
}

// unattendedPeer scripts a peer runner that reports unattended.
func unattendedPeer() *MockRunner {
	return NewMockRunner().OnSSH("mesh.presence", presenceReply(false), nil)
}

// deadPeer scripts an unreachable peer: every probe errors with empty output.
func deadPeer() *MockRunner {
	return NewMockRunner().DefaultSSH("", errors.New("ssh: connect timed out"))
}

// seedMesh isolates HOME to a temp dir and seeds the shared registry and exact
// product-owned route state. Route reads both fresh each call.
func seedMesh(t *testing.T, self string, routeOff bool, hosts ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("DAEMONKIT_HOME", dir)
	if err := Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mesh: %v", err)
	}
	if _, err := Config.Update(context.Background(), func(g *hostregistry.Registry) error {
		g.Self = self
		return nil
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	for _, host := range hosts {
		fact, err := hostregistry.NewSSHHostFact(host, "/opt/homebrew/bin/synckitd", nil)
		if err != nil {
			t.Fatalf("host fact: %v", err)
		}
		if err := Config.RegisterHost(context.Background(), fact); err != nil {
			t.Fatalf("register host: %v", err)
		}
	}
	if err := SetRouteOff(context.Background(), routeOff); err != nil {
		t.Fatalf("seed route off: %v", err)
	}
}

func mockDialer(runners map[string]*MockRunner) func(string) Runner {
	return func(target string) Runner {
		r, ok := runners[target]
		if !ok {
			panic("unscripted dial target: " + target)
		}
		return r
	}
}

// notifyCall returns the interaction.notify surface a runner received, failing
// when it received none.
func notifyCall(t *testing.T, r *MockRunner, target string) MockCall {
	t.Helper()
	for _, c := range r.SSHCalls(target) {
		if strings.Contains(c.Cmd, "interaction.notify") {
			return c
		}
	}
	t.Fatalf("runner received no interaction.notify surface against %q: %v", target, r.SSHCmdsAll())
	return MockCall{}
}

func surfaced(r *MockRunner) bool {
	return sshCmdContains(r, "interaction.notify")
}

// TestRouteAttendedLocalNoWalk proves an attended local console short-circuits
// before any peer probe — the human is already here.
func TestRouteAttendedLocalNoWalk(t *testing.T) {
	seedMesh(t, "me@here", false, "u@peer")
	peer := attendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@peer": peer}),
		Attended: attendedFn(true),
	}
	target, err := r.Route(context.Background(), "subj-1", 1, "Deploy?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (local attended)", target)
	}
	if len(peer.SSHCmdsAll()) != 0 {
		t.Fatalf("attended local still walked peers: %v", peer.SSHCmdsAll())
	}
}

// TestRouteFirstAttendedWins proves an unattended host walks peers in registry
// order and surfaces on the first attended one, leaving later attended peers
// unsurfaced.
func TestRouteFirstAttendedWins(t *testing.T) {
	seedMesh(t, "me@origin.tail.ts.net", false, "u@p1", "u@p2", "u@p3")
	p1, p2, p3 := unattendedPeer(), attendedPeer(), attendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@p1": p1, "u@p2": p2, "u@p3": p3}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-9", 9, "Ship it?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "u@p2" {
		t.Fatalf("target = %q, want u@p2 (first attended in order)", target)
	}
	if !surfaced(p2) {
		t.Fatal("first attended peer p2 was not surfaced")
	}
	if surfaced(p1) || surfaced(p3) {
		t.Fatalf("a non-winning peer was surfaced (p1=%v p3=%v)", surfaced(p1), surfaced(p3))
	}
	call := notifyCall(t, p2, "u@p2")
	if call.Kind != "ssh" {
		t.Fatalf("surface kind = %q, want canonical failover ssh", call.Kind)
	}
	var got interaction.NotificationPayload
	if err := json.Unmarshal([]byte(call.Stdin), &got); err != nil {
		t.Fatalf("decode surfaced payload off stdin: %v", err)
	}
	if got.DeliveryKey != "routed:me@origin.tail.ts.net:subj-9:9" {
		t.Fatalf("delivery key = %q, want stable origin event key", got.DeliveryKey)
	}
	if got.Urgency != "" {
		t.Fatalf("surfaced urgency = %q, want empty so the peer never re-routes it", got.Urgency)
	}
	if !strings.Contains(got.Message, "origin") || !strings.Contains(got.Message, "subj-9") {
		t.Fatalf("surfaced message %q missing origin host or subject", got.Message)
	}
	if strings.Contains(call.Cmd, "needs you") {
		t.Fatalf("surface cmd %q leaked the payload onto argv", call.Cmd)
	}
	// The surface stamps the origin identity so peer-side subjects never collide
	// across origin sessions.
	wantSession := "--session " + hostregistry.ShellQuote("routed:me@origin.tail.ts.net:subj-9")
	if !strings.Contains(call.Cmd, wantSession) {
		t.Fatalf("surface cmd missing %q; calls = %v", wantSession, p2.SSHCmdsAll())
	}
}

// TestRouteFallsOverToNextAttendedPeer proves a failed surface on the first
// attended peer falls over to the next attended peer instead of dropping the
// route.
func TestRouteFallsOverToNextAttendedPeer(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@p1", "u@p2")
	p1 := NewMockRunner().
		OnSSH("mesh.presence", presenceReply(true), nil).
		OnSSH("interaction.notify", "", errors.New("ssh: broken pipe"))
	p2 := attendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@p1": p1, "u@p2": p2}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-10", 10, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "u@p2" {
		t.Fatalf("target = %q, want u@p2 (fallback after p1's failed surface)", target)
	}
	if !surfaced(p1) || !surfaced(p2) {
		t.Fatalf("surface attempts p1=%v p2=%v, want both in order", surfaced(p1), surfaced(p2))
	}
	first, second := notifyCall(t, p1, "u@p1"), notifyCall(t, p2, "u@p2")
	var firstNote, secondNote interaction.NotificationPayload
	if err := json.Unmarshal([]byte(first.Stdin), &firstNote); err != nil {
		t.Fatalf("decode p1 notification: %v", err)
	}
	if err := json.Unmarshal([]byte(second.Stdin), &secondNote); err != nil {
		t.Fatalf("decode p2 notification: %v", err)
	}
	if firstNote.DeliveryKey == "" || firstNote.DeliveryKey != secondNote.DeliveryKey {
		t.Fatalf("failover delivery keys = (%q, %q), want one stable key", firstNote.DeliveryKey, secondNote.DeliveryKey)
	}
}

// wedgedSurfaceRunner reports attended instantly but wedges every notify until
// its context dies, like a peer whose daemon hangs mid-surface.
type wedgedSurfaceRunner struct{}

func (wedgedSurfaceRunner) Local(context.Context, []byte, string, ...string) (string, error) {
	panic("wedgedSurfaceRunner is ssh-only")
}

func (wedgedSurfaceRunner) SSH(ctx context.Context, _, remoteCmd string, _ []byte) (string, error) {
	return wedgeOrPresence(ctx, remoteCmd)
}

func wedgeOrPresence(ctx context.Context, remoteCmd string) (string, error) {
	if strings.Contains(remoteCmd, "mesh.presence") {
		return presenceReply(true), nil
	}
	<-ctx.Done()
	return "", ctx.Err()
}

// TestRouteWedgedSurfaceFallsOverWithLiveContext proves a first surface that
// hangs to its per-attempt deadline still leaves the next attended peer a live
// context — the failover is not attempted with an already-cancelled context.
func TestRouteWedgedSurfaceFallsOverWithLiveContext(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@wedged", "u@live")
	live := attendedPeer()
	r := Router{
		Local: NewMockRunner(),
		Dial: func(target string) Runner {
			if target == "u@live" {
				return live
			}
			return wedgedSurfaceRunner{}
		},
		Attended:       attendedFn(false),
		attemptTimeout: 50 * time.Millisecond,
	}
	target, err := r.Route(context.Background(), "subj-13", 13, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "u@live" {
		t.Fatalf("target = %q, want u@live after the wedged surface timed out", target)
	}
	if !surfaced(live) {
		t.Fatal("failover surface never reached the live peer")
	}
}

// TestRouteAllSurfacesFailReturnsError proves every attended peer's failed
// surface is returned, never silently dropped.
func TestRouteAllSurfacesFailReturnsError(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@p1", "u@p2")
	failing := func() *MockRunner {
		return NewMockRunner().
			OnSSH("mesh.presence", presenceReply(true), nil).
			OnSSH("interaction.notify", "", errors.New("ssh: broken pipe"))
	}
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@p1": failing(), "u@p2": failing()}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-11", 11, "?")
	if target != "" || err == nil {
		t.Fatalf("target=%q err=%v, want no surface and the joined failures", target, err)
	}
	for _, node := range []string{"p1", "p2"} {
		if !strings.Contains(err.Error(), node) {
			t.Fatalf("err = %v, want both failed peers named", err)
		}
	}
}

// slowRunner blocks every probe until its context cancels, simulating a wedged
// peer.
type slowRunner struct{}

func (slowRunner) Local(context.Context, []byte, string, ...string) (string, error) {
	panic("slowRunner is ssh-only")
}

func (slowRunner) SSH(ctx context.Context, _, _ string, _ []byte) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestRouteSlowProbeDoesNotDelayDecidedPrefix proves routing surfaces on an
// already-decided attended peer without waiting for a slower peer's probe.
func TestRouteSlowProbeDoesNotDelayDecidedPrefix(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@fast", "u@slow")
	fast := attendedPeer()
	r := Router{
		Local: NewMockRunner(),
		Dial: func(target string) Runner {
			if target == "u@fast" {
				return fast
			}
			return slowRunner{}
		},
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-12", 12, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "u@fast" {
		t.Fatalf("target = %q, want u@fast without waiting on the wedged peer", target)
	}
}

// TestRouteDeadPeerSkipped proves an unreachable peer is skipped rather than
// aborting the walk — routing lands on the next attended peer.
func TestRouteDeadPeerSkipped(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@dead", "u@live")
	dead, live := deadPeer(), attendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@dead": dead, "u@live": live}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-2", 2, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "u@live" {
		t.Fatalf("target = %q, want u@live (dead peer skipped)", target)
	}
}

// TestRouteAllUnattendedFallsBack proves an unattended host with no attended peer
// (all dead or unattended) falls back with no surface.
func TestRouteAllUnattendedFallsBack(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@dead", "u@off")
	dead, off := deadPeer(), unattendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@dead": dead, "u@off": off}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-3", 3, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (nobody attended)", target)
	}
	if surfaced(off) {
		t.Fatal("surfaced on an unattended peer")
	}
}

// TestRouteOffDisablesWalk proves the escape hatch skips routing entirely while
// leaving the peers registered.
func TestRouteOffDisablesWalk(t *testing.T) {
	seedMesh(t, "me@origin", true, "u@peer")
	peer := attendedPeer()
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@peer": peer}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-4", 4, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (route off)", target)
	}
	if len(peer.SSHCmdsAll()) != 0 {
		t.Fatalf("route off still probed peers: %v", peer.SSHCmdsAll())
	}
}

// TestRouteNoPeersFallsBack proves an empty registry routes nowhere.
func TestRouteNoPeersFallsBack(t *testing.T) {
	seedMesh(t, "me@origin", false)
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{}),
		Attended: attendedFn(false),
	}
	target, err := r.Route(context.Background(), "subj-5", 5, "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (no peers)", target)
	}
}

// TestRouteSurfaceErrorPropagates proves a surface that fails on the chosen peer
// returns the error — a surface that does not land is never silently dropped.
func TestRouteSurfaceErrorPropagates(t *testing.T) {
	seedMesh(t, "me@origin", false, "u@peer")
	peer := NewMockRunner().
		OnSSH("mesh.presence", presenceReply(true), nil).
		OnSSH("interaction.notify", "", errors.New("ssh: broken pipe"))
	r := Router{
		Local:    NewMockRunner(),
		Dial:     mockDialer(map[string]*MockRunner{"u@peer": peer}),
		Attended: attendedFn(false),
	}
	if _, err := r.Route(context.Background(), "subj-6", 6, "?"); err == nil {
		t.Fatal("Route returned nil, want the failed surface propagated")
	}
}

// TestRoutedNotificationShape proves the surface names the origin machine, subject,
// and header, carries no bearer token, and has an empty urgency so the receiving
// peer never bounces it onward.
func TestRoutedNotificationShape(t *testing.T) {
	n := RoutedNotification("alice@mac.tail.ts.net", "interaction-abcd", 42, "Approve deploy?")
	if n.Urgency != "" {
		t.Fatalf("urgency = %q, want empty", n.Urgency)
	}
	for _, want := range []string{"mac", "interaction-abcd", "Approve deploy?"} {
		if !strings.Contains(n.Message, want) {
			t.Fatalf("message %q missing %q", n.Message, want)
		}
	}
	for _, forbidden := range []string{"token", "bearer", "Authorization"} {
		if strings.Contains(strings.ToLower(n.Message), strings.ToLower(forbidden)) {
			t.Fatalf("message %q leaked %q", n.Message, forbidden)
		}
	}
	if n.DeliveryKey != "routed:alice@mac.tail.ts.net:interaction-abcd:42" {
		t.Fatalf("delivery key = %q", n.DeliveryKey)
	}
	b, _ := json.Marshal(n)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("marshal round-trip: %v", err)
	}
	if _, ok := raw["message"]; !ok || len(raw) != 2 {
		t.Fatalf("payload fields = %v, want message and delivery_key", raw)
	}
}

// TestRoutingEnabled table-drives the config guard.
func TestRoutingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		hosts    []string
		routeOff bool
		want     bool
	}{
		{"peers, routing on", []string{"u@a"}, false, true},
		{"peers, routing off", []string{"u@a"}, true, false},
		{"no peers", nil, false, false},
		{"no peers, off is moot", nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := &hostregistry.Registry{Hosts: tc.hosts}
			if got := RoutingEnabled(reg, tc.routeOff); got != tc.want {
				t.Fatalf("RoutingEnabled(%v, %v) = %v, want %v", tc.hosts, tc.routeOff, got, tc.want)
			}
		})
	}
}
