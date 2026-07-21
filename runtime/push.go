package runtime

import (
	"context"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
)

// pushTimeout bounds one background push fan-out across every delivery lane.
const pushTimeout = 30 * time.Second

// routeTimeout bounds one background presence-routing attempt: the peer presence
// walk over ssh plus the surface on the chosen peer.
const routeTimeout = 30 * time.Second

// deviceTokenPattern matches a ≥64-char lowercase-hex run — the shape of an
// APNs device token, a delivery credential that must never log in full.
var deviceTokenPattern = regexp.MustCompile("[0-9a-f]{64,}")

// redactDeviceTokens scrubs token-shaped runs from a lane error's message
// before it logs — the last line of defense under the senders' own redaction.
func redactDeviceTokens(msg string) string {
	return deviceTokenPattern.ReplaceAllStringFunc(msg, func(m string) string {
		return m[:8] + "…"
	})
}

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
	mu         sync.RWMutex
	senders    []pushSender
	background func(func(context.Context))
	router     interactionRouter
}

func (f *pushFanout) setSenders(senders []pushSender) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.senders = append([]pushSender(nil), senders...)
}

func (f *pushFanout) Question(subjectID string, q interaction.QuestionPayload) {
	f.deliver(access.PushPayload{
		Type:    interaction.EventQuestion,
		Subject: subjectID,
		Title:   q.Header,
		Body:    q.Prompt,
		Urgency: access.PushUrgencyHigh,
	})
	f.route(subjectID, q.Header)
}

func (f *pushFanout) Notification(subjectID string, n interaction.NotificationPayload) {
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
func (f *pushFanout) deliver(p access.PushPayload) {
	f.mu.RLock()
	senders := append([]pushSender(nil), f.senders...)
	f.mu.RUnlock()
	for _, s := range senders {
		f.background(func(ctx context.Context) {
			ctx, cancel := context.WithTimeout(ctx, pushTimeout)
			defer cancel()
			if err := s.Fanout(ctx, p); err != nil {
				log.Printf("[%s] push fanout: %s", interaction.AppName, redactDeviceTokens(err.Error()))
			}
		})
	}
}

// route surfaces the interaction on an attended peer off the handler's goroutine.
// It is best-effort and additive: any error only logs, and the push lanes above
// have already fired regardless of the routing outcome.
func (f *pushFanout) route(subjectID, header string) {
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
			log.Printf("[%s] surfaced subject %s on attended peer %s", interaction.AppName, subjectID, hostregistry.HostNode(target))
		}
	})
}
