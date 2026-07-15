package runtime

import (
	"context"
	"log"
	"time"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
)

// pushTimeout bounds one background push fan-out across every delivery lane.
const pushTimeout = 30 * time.Second

// pushSender is one delivery lane fanning a frame out to its registered
// endpoints: Web Push subscriptions, APNs device tokens.
type pushSender interface {
	Fanout(context.Context, access.PushPayload) error
}

// pushFanout bridges interaction's append fan-out to the access plane's push
// lanes: each hook maps the domain payload onto the compact push frame and
// delivers it off the handler's goroutine, tracked on the daemon lifecycle
// via background (daemon.Server.Background).
type pushFanout struct {
	senders    []pushSender
	background func(func(context.Context))
}

func (f pushFanout) Question(subjectID string, q interaction.QuestionPayload) {
	f.deliver(access.PushPayload{
		Type:    interaction.EventQuestion,
		Subject: subjectID,
		Title:   q.Header,
		Body:    q.Prompt,
		Urgency: access.PushUrgencyHigh,
	})
}

func (f pushFanout) Notification(subjectID string, n interaction.NotificationPayload) {
	f.deliver(access.PushPayload{
		Type:    interaction.EventNotification,
		Subject: subjectID,
		Body:    n.Message,
		Urgency: n.Urgency,
	})
}

// deliver runs every lane's fan-out off the handler's goroutine on the daemon
// lifecycle: the serve context cancels in-flight sends at shutdown, and the
// daemon drains them before closing the store. One lane's failure never
// blocks another's delivery.
func (f pushFanout) deliver(p access.PushPayload) {
	f.background(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, pushTimeout)
		defer cancel()
		for _, s := range f.senders {
			if err := s.Fanout(ctx, p); err != nil {
				log.Printf("[%s] push fanout: %v", interaction.AppName, err)
			}
		}
	})
}
