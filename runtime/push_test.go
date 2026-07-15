package runtime

import (
	"context"
	"sync"
	"testing"

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
