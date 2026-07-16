package runtime

import (
	"context"
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
