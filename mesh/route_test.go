package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

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
	return NewMockRunner().On("mesh.presence", presenceReply(true), nil).On("interaction.notify", okNotifyReply(), nil)
}

// unattendedPeer scripts a peer runner that reports unattended.
func unattendedPeer() *MockRunner {
	return NewMockRunner().On("mesh.presence", presenceReply(false), nil)
}

// deadPeer scripts an unreachable peer: every probe errors with empty output.
func deadPeer() *MockRunner {
	return NewMockRunner().Default("", errors.New("ssh: connect timed out"))
}

func seedStore(t *testing.T, self string, routeOff bool, hosts ...string) Store {
	t.Helper()
	s := Store{Dir: t.TempDir()}
	if _, err := s.Update(context.Background(), func(g *Registry) error {
		g.Self = self
		for _, h := range hosts {
			g.UpsertHost(h)
		}
		g.RouteOff = routeOff
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return s
}

func dialer(runners map[string]*MockRunner) func(string) Runner {
	return func(target string) Runner {
		r, ok := runners[target]
		if !ok {
			panic("unscripted dial target: " + target)
		}
		return r
	}
}

// notifyPayload extracts the NotificationPayload a runner was asked to surface,
// failing when it received no interaction.notify call.
func notifyPayload(t *testing.T, r *MockRunner) interaction.NotificationPayload {
	t.Helper()
	for _, call := range r.Calls() {
		if !strings.Contains(strings.Join(call, " "), "interaction.notify") {
			continue
		}
		for i, a := range call {
			if a == "--json" && i+1 < len(call) {
				var n interaction.NotificationPayload
				if err := json.Unmarshal([]byte(call[i+1]), &n); err != nil {
					t.Fatalf("decode surfaced payload: %v", err)
				}
				return n
			}
		}
	}
	t.Fatalf("runner received no interaction.notify surface: %v", r.Calls())
	return interaction.NotificationPayload{}
}

func surfaced(r *MockRunner) bool {
	for _, call := range r.Calls() {
		if strings.Contains(strings.Join(call, " "), "interaction.notify") {
			return true
		}
	}
	return false
}

// TestRouteAttendedLocalNoWalk proves an attended local console short-circuits
// before any peer probe — the human is already here.
func TestRouteAttendedLocalNoWalk(t *testing.T) {
	me := currentUser(t)
	peer := attendedPeer()
	r := Router{
		Store: seedStore(t, "me@here", false, "u@peer"),
		Local: darwinRunner(ioregPlist(false, true, false, me), netstatHeaderLine),
		Dial:  dialer(map[string]*MockRunner{"u@peer": peer}),
		GOOS:  osDarwin,
	}
	target, err := r.Route(context.Background(), "subj-1", "Deploy?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (local attended)", target)
	}
	if len(peer.Calls()) != 0 {
		t.Fatalf("attended local still walked peers: %v", peer.Calls())
	}
}

// TestRouteFirstAttendedWins proves an unattended host walks peers in registry
// order and surfaces on the first attended one, leaving later attended peers
// unsurfaced.
func TestRouteFirstAttendedWins(t *testing.T) {
	p1, p2, p3 := unattendedPeer(), attendedPeer(), attendedPeer()
	runners := map[string]*MockRunner{"u@p1": p1, "u@p2": p2, "u@p3": p3}
	r := Router{
		Store: seedStore(t, "me@origin.tail.ts.net", false, "u@p1", "u@p2", "u@p3"),
		Local: NewMockRunner(),
		Dial:  dialer(runners),
		GOOS:  "linux", // local unattended by degradation
	}
	target, err := r.Route(context.Background(), "subj-9", "Ship it?")
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
	got := notifyPayload(t, p2)
	if got.Urgency != "" {
		t.Fatalf("surfaced urgency = %q, want empty so the peer never re-routes it", got.Urgency)
	}
	if !strings.Contains(got.Message, "origin") || !strings.Contains(got.Message, "subj-9") {
		t.Fatalf("surfaced message %q missing origin host or subject", got.Message)
	}
}

// TestRouteDeadPeerSkipped proves an unreachable peer is skipped rather than
// aborting the walk — routing lands on the next attended peer.
func TestRouteDeadPeerSkipped(t *testing.T) {
	dead, live := deadPeer(), attendedPeer()
	r := Router{
		Store: seedStore(t, "me@origin", false, "u@dead", "u@live"),
		Local: NewMockRunner(),
		Dial:  dialer(map[string]*MockRunner{"u@dead": dead, "u@live": live}),
		GOOS:  "linux",
	}
	target, err := r.Route(context.Background(), "subj-2", "?")
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
	dead, off := deadPeer(), unattendedPeer()
	r := Router{
		Store: seedStore(t, "me@origin", false, "u@dead", "u@off"),
		Local: NewMockRunner(),
		Dial:  dialer(map[string]*MockRunner{"u@dead": dead, "u@off": off}),
		GOOS:  "linux",
	}
	target, err := r.Route(context.Background(), "subj-3", "?")
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
	peer := attendedPeer()
	r := Router{
		Store: seedStore(t, "me@origin", true, "u@peer"),
		Local: NewMockRunner(),
		Dial:  dialer(map[string]*MockRunner{"u@peer": peer}),
		GOOS:  "linux",
	}
	target, err := r.Route(context.Background(), "subj-4", "?")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if target != "" {
		t.Fatalf("target = %q, want no surface (route off)", target)
	}
	if len(peer.Calls()) != 0 {
		t.Fatalf("route off still probed peers: %v", peer.Calls())
	}
}

// TestRouteNoPeersFallsBack proves an empty registry routes nowhere.
func TestRouteNoPeersFallsBack(t *testing.T) {
	r := Router{
		Store: seedStore(t, "me@origin", false),
		Local: NewMockRunner(),
		Dial:  dialer(map[string]*MockRunner{}),
		GOOS:  "linux",
	}
	target, err := r.Route(context.Background(), "subj-5", "?")
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
	peer := NewMockRunner().
		On("mesh.presence", presenceReply(true), nil).
		On("interaction.notify", "", errors.New("ssh: broken pipe"))
	r := Router{
		Store: seedStore(t, "me@origin", false, "u@peer"),
		Local: NewMockRunner(),
		Dial:  dialer(map[string]*MockRunner{"u@peer": peer}),
		GOOS:  "linux",
	}
	if _, err := r.Route(context.Background(), "subj-6", "?"); err == nil {
		t.Fatal("Route returned nil, want the failed surface propagated")
	}
}

// TestRoutedNotificationShape proves the surface names the origin machine, subject,
// and header, carries no bearer token, and has an empty urgency so the receiving
// peer never bounces it onward.
func TestRoutedNotificationShape(t *testing.T) {
	n := RoutedNotification("alice@mac.tail.ts.net", "interaction-abcd", "Approve deploy?")
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
	// Marshaled, the payload is exactly {message,urgency} — no field can carry a
	// secret.
	b, _ := json.Marshal(n)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("marshal round-trip: %v", err)
	}
	if _, ok := raw["message"]; !ok || len(raw) != 1 {
		t.Fatalf("payload fields = %v, want only message (urgency omitempty)", raw)
	}
}

// TestRoutingEnabled table-drives the config guard.
func TestRoutingEnabled(t *testing.T) {
	tests := []struct {
		name string
		reg  Registry
		want bool
	}{
		{"peers, routing on", Registry{Hosts: []string{"u@a"}}, true},
		{"peers, routing off", Registry{Hosts: []string{"u@a"}, RouteOff: true}, false},
		{"no peers", Registry{}, false},
		{"no peers, off is moot", Registry{RouteOff: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoutingEnabled(&tc.reg); got != tc.want {
				t.Fatalf("RoutingEnabled(%+v) = %v, want %v", tc.reg, got, tc.want)
			}
		})
	}
}
