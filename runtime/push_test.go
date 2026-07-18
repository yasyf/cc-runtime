package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
)

// recordingSender captures every payload one delivery lane is asked to fan
// out.
type recordingSender struct {
	mu       sync.Mutex
	payloads []access.PushPayload
}

func (r *recordingSender) Fanout(_ context.Context, p access.PushPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, p)
	return nil
}

func (r *recordingSender) recorded() []access.PushPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]access.PushPayload(nil), r.payloads...)
}

// routeCall is one interaction the router was asked to surface on a peer.
type routeCall struct {
	subjectID string
	header    string
}

// recordingRouter records every routing request and returns a scripted outcome.
type recordingRouter struct {
	mu     sync.Mutex
	calls  []routeCall
	target string
	err    error
}

func (r *recordingRouter) Route(_ context.Context, subjectID, header string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, routeCall{subjectID, header})
	return r.target, r.err
}

func (r *recordingRouter) recorded() []routeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]routeCall(nil), r.calls...)
}

// stuckSender blocks its fan-out until released or its lane context expires,
// standing in for a hung push endpoint.
type stuckSender struct {
	release chan struct{}
}

func (s *stuckSender) Fanout(ctx context.Context, _ access.PushPayload) error {
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return ctx.Err()
}

func TestPushFanoutMapsAppendsOntoEveryLane(t *testing.T) {
	webPush, apns := &recordingSender{}, &recordingSender{}
	f := pushFanout{
		senders:    []pushSender{webPush, apns},
		background: func(fn func(context.Context)) { fn(context.Background()) },
	}

	f.Question("s1", interaction.QuestionPayload{Header: "Deploy?", Prompt: "pick one"})
	f.Notification("s1", interaction.NotificationPayload{Message: "done", Urgency: "normal"})

	want := []access.PushPayload{
		{Type: interaction.EventQuestion, Subject: "s1", Title: "Deploy?", Body: "pick one", Urgency: access.PushUrgencyHigh},
		{Type: interaction.EventNotification, Subject: "s1", Body: "done", Urgency: "normal"},
	}
	for id, lane := range map[string]*recordingSender{"web push": webPush, "apns": apns} {
		got := lane.recorded()
		if len(got) != len(want) {
			t.Fatalf("%s lane received %d payloads, want %d", id, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s lane payload %d = %+v, want %+v", id, i, got[i], want[i])
			}
		}
	}
}

// TestPushFanoutRoutesQuestionsAndHighNotifications proves the presence router is
// invoked for a question and an urgency-high notification but not a normal one,
// while the push lanes fire for every event regardless of the routing outcome.
func TestPushFanoutRoutesQuestionsAndHighNotifications(t *testing.T) {
	web := &recordingSender{}
	router := &recordingRouter{target: "u@peer"}
	f := pushFanout{
		senders:    []pushSender{web},
		background: func(fn func(context.Context)) { fn(context.Background()) },
		router:     router,
	}

	f.Question("s1", interaction.QuestionPayload{Header: "Deploy?", Prompt: "pick one"})
	f.Notification("s2", interaction.NotificationPayload{Message: "urgent", Urgency: access.PushUrgencyHigh})
	f.Notification("s3", interaction.NotificationPayload{Message: "fyi", Urgency: "normal"})

	if got := len(web.recorded()); got != 3 {
		t.Fatalf("push lane fired %d times, want 3 (every event pushes regardless of routing)", got)
	}
	want := []routeCall{{"s1", "Deploy?"}, {"s2", "urgent"}}
	got := router.recorded()
	if len(got) != len(want) {
		t.Fatalf("router saw %+v, want %+v (normal notification must not route)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestPushFanoutPushesEvenWhenRouteFails proves a failing route never suppresses
// the push lanes — presence routing is additive, phones fire regardless.
func TestPushFanoutPushesEvenWhenRouteFails(t *testing.T) {
	web := &recordingSender{}
	router := &recordingRouter{err: context.DeadlineExceeded}
	f := pushFanout{
		senders:    []pushSender{web},
		background: func(fn func(context.Context)) { fn(context.Background()) },
		router:     router,
	}

	f.Question("s1", interaction.QuestionPayload{Header: "Deploy?", Prompt: "pick one"})

	if got := len(web.recorded()); got != 1 {
		t.Fatalf("push lane fired %d times, want 1 despite the route failure", got)
	}
	if got := len(router.recorded()); got != 1 {
		t.Fatalf("router invoked %d times, want 1", got)
	}
}

// TestPushFanoutNilRouterSkipsRouting proves the local-only path (no mesh router)
// still fires the push lanes and never dereferences a nil router.
func TestPushFanoutNilRouterSkipsRouting(t *testing.T) {
	web := &recordingSender{}
	f := pushFanout{
		senders:    []pushSender{web},
		background: func(fn func(context.Context)) { fn(context.Background()) },
	}

	f.Question("s1", interaction.QuestionPayload{Header: "Deploy?"})
	f.Notification("s2", interaction.NotificationPayload{Message: "hi", Urgency: access.PushUrgencyHigh})

	if got := len(web.recorded()); got != 2 {
		t.Fatalf("push lane fired %d times, want 2", got)
	}
}

// TestPushFanoutLanesDeliverIndependently asserts a lane blocked to its
// deadline cannot starve the one behind it: the APNs lane must receive its
// payload while the Web Push lane is still hung.
func TestPushFanoutLanesDeliverIndependently(t *testing.T) {
	stuck := &stuckSender{release: make(chan struct{})}
	apns := &recordingSender{}
	var wg sync.WaitGroup
	f := pushFanout{
		senders: []pushSender{stuck, apns},
		background: func(fn func(context.Context)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn(context.Background())
			}()
		},
	}

	f.Notification("s1", interaction.NotificationPayload{Message: "done", Urgency: "normal"})

	deadline := time.After(5 * time.Second)
	for len(apns.recorded()) == 0 {
		select {
		case <-deadline:
			t.Fatal("apns lane starved: no delivery while the web push lane is blocked")
		case <-time.After(time.Millisecond):
		}
	}
	close(stuck.release)
	wg.Wait()

	got := apns.recorded()
	want := access.PushPayload{Type: interaction.EventNotification, Subject: "s1", Body: "done", Urgency: "normal"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("apns lane recorded %+v, want [%+v]", got, want)
	}
}

// TestRedactDeviceTokensScrubsTokenShapedRuns pins the log line's last-line
// defense: a token-shaped hex run in a lane error never logs in full.
func TestRedactDeviceTokensScrubsTokenShapedRuns(t *testing.T) {
	token := strings.Repeat("ab", 32)
	msg := "apns: dial https://host/3/device/" + token + ": connection refused"
	got := redactDeviceTokens(msg)
	if strings.Contains(got, token) {
		t.Fatalf("redactDeviceTokens(%q) = %q, want the full token scrubbed", msg, got)
	}
	if want := "apns: dial https://host/3/device/" + token[:8] + "…: connection refused"; got != want {
		t.Fatalf("redactDeviceTokens = %q, want %q", got, want)
	}
	if benign := "reason InvalidProviderToken status 403 deadbeef"; redactDeviceTokens(benign) != benign {
		t.Fatalf("redactDeviceTokens(%q) rewrote a non-token-shaped message", benign)
	}
}
