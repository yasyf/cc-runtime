package runtime

import (
	"context"
	"log"
	"time"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

// pushTimeout bounds one background push fan-out across every delivery lane.
const pushTimeout = 30 * time.Second

// routeTimeout bounds one background presence-routing attempt: the peer presence
// walk over ssh plus the surface on the chosen peer.
const routeTimeout = 30 * time.Second

// pushSender is one delivery lane fanning a frame out to its registered
// endpoints: Web Push subscriptions, APNs device tokens.
type pushSender interface {
	Fanout(context.Context, access.PushPayload) error
}

// interactionRouter surfaces an interaction on an attended peer when this host is
// unattended, additively to the push lanes. mesh.Router implements it; a nil
// router disables routing, keeping the local-only path unchanged.
type interactionRouter interface {
	Route(ctx context.Context, subjectID, header string) (string, error)
}

// pushFanout bridges interaction's append fan-out to the access plane's push
// lanes and the mesh presence router: each hook maps the domain payload onto the
// compact push frame, delivers it off the handler's goroutine, and — for a
// question or urgency-high notification — additively surfaces it on an attended
// peer when this host is unattended. Everything runs on the daemon lifecycle via
// background (daemon.Server.Background).
type pushFanout struct {
	senders    []pushSender
	background func(func(context.Context))
	router     interactionRouter
}

func (f pushFanout) Question(subjectID string, q interaction.QuestionPayload) {
	f.deliver(access.PushPayload{
		Type:    interaction.EventQuestion,
		Subject: subjectID,
		Title:   q.Header,
		Body:    q.Prompt,
		Urgency: access.PushUrgencyHigh,
	})
	f.route(subjectID, q.Header)
}

func (f pushFanout) Notification(subjectID string, n interaction.NotificationPayload) {
	f.deliver(access.PushPayload{
		Type:    interaction.EventNotification,
		Subject: subjectID,
		Body:    n.Message,
		Urgency: n.Urgency,
	})
	if n.Urgency == access.PushUrgencyHigh {
		f.route(subjectID, n.Message)
	}
}

// deliver runs each lane's fan-out on its own goroutine with its own deadline,
// off the handler's goroutine on the daemon lifecycle: the serve context
// cancels in-flight sends at shutdown, and the daemon drains them before
// closing the store. One lane blocking to its deadline never delays or starves
// another's delivery.
func (f pushFanout) deliver(p access.PushPayload) {
	for _, s := range f.senders {
		f.background(func(ctx context.Context) {
			ctx, cancel := context.WithTimeout(ctx, pushTimeout)
			defer cancel()
			if err := s.Fanout(ctx, p); err != nil {
				log.Printf("[%s] push fanout: %v", interaction.AppName, err)
			}
		})
	}
}

// route surfaces the interaction on an attended peer off the handler's goroutine.
// It is best-effort and additive: any error only logs, and the push lanes above
// have already fired regardless of the routing outcome.
func (f pushFanout) route(subjectID, header string) {
	if f.router == nil {
		return
	}
	f.background(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, routeTimeout)
		defer cancel()
		target, err := f.router.Route(ctx, subjectID, header)
		if err != nil {
			log.Printf("[%s] presence route: %v", interaction.AppName, err)
			return
		}
		if target != "" {
			log.Printf("[%s] surfaced subject %s on attended peer %s", interaction.AppName, subjectID, mesh.HostNode(target))
		}
	})
}
