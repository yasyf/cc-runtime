package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
)

// RoutingEnabled reports whether presence routing is active: at least one
// registered peer and not switched off (`mesh route off`).
func RoutingEnabled(reg *hostregistry.Registry, routeOff bool) bool {
	return len(reg.Hosts) > 0 && !routeOff
}

// RoutedNotification composes the surface a peer receives for an interaction that
// fired on an unattended origin: an origin-tagged notification naming the machine,
// the subject, and the question header, so the human at the peer knows where to
// look. It carries no bearer token — answering happens against the origin over the
// mesh answer path — and its urgency is deliberately empty so the receiving peer
// never treats it as a routable event and bounces it onward.
func RoutedNotification(originHost, subjectID, header string) interaction.NotificationPayload {
	node := hostregistry.HostNode(originHost)
	msg := fmt.Sprintf("[%s] needs you: %s (subject %s on %s)", node, header, subjectID, node)
	return interaction.NotificationPayload{Message: msg}
}

// Router surfaces an interaction on an attended peer when this host is unattended.
// It is additive to the always-on push lanes: routing only adds a peer-machine
// surface, never replaces the phones a push reaches wherever they are.
type Router struct {
	Local Runner
	Dial  func(target string) Runner
	// Attended reads this host's local console attendance; injected so tests drive
	// routing without the real console probe. NewRouter defaults it to the synckit
	// presence read.
	Attended func(context.Context) (bool, error)
	// attemptTimeout is test-only injection for one peer probe or surface.
	attemptTimeout time.Duration
}

// NewRouter builds a Router probing this host over local and dialing peers over
// ssh, with the default synckit presence read for local attendance.
func NewRouter(local Runner, dial func(target string) Runner) Router {
	return Router{Local: local, Dial: dial, Attended: localAttended}
}

// defaultAttemptTimeout bounds one peer probe or surface, so a wedged peer burns a
// slice of the route budget rather than all of it — the failover to the next
// attended peer still runs inside the caller's deadline.
const defaultAttemptTimeout = 10 * time.Second

func (r Router) peerAttemptTimeout() time.Duration {
	if r.attemptTimeout > 0 {
		return r.attemptTimeout
	}
	return defaultAttemptTimeout
}

// localAttended reads this host's console attendance from the synckit presence
// probe.
func localAttended(ctx context.Context) (bool, error) {
	p, err := DetectPresence(ctx)
	if err != nil {
		return false, err
	}
	return p.Attended, nil
}

// Route surfaces the interaction (subjectID, header) on the first attended peer
// when this host is unattended and routing is enabled. It returns the peer it
// surfaced on, or "" when it fell back: routing off, no peers, this host attended
// (the human is already here), or no peer attended anywhere. The registry and the
// route-off flag are read fresh each call so `host add`/`rm` and `route off` take
// effect without a daemon restart. Routing is best-effort — the caller has already
// fired the push lanes — so an error is returned for logging, never to block
// delivery.
func (r Router) Route(ctx context.Context, subjectID, header string) (string, error) {
	reg, err := Config.Load()
	if err != nil {
		return "", err
	}
	off, err := LoadRouteOff()
	if err != nil {
		return "", err
	}
	if !RoutingEnabled(reg, off) {
		return "", nil
	}
	attended, err := r.Attended(ctx)
	if err != nil {
		return "", err
	}
	if attended {
		return "", nil
	}
	return r.surfaceFirstAttended(ctx, reg, routedSession(reg.Self, subjectID), RoutedNotification(reg.Self, subjectID, header))
}

// routedSession is the window identity a routed surface stamps on the peer:
// distinct per (origin host, origin subject), so surfaces from different origin
// sessions never collide into one peer-side subject.
func routedSession(originHost, subjectID string) string {
	return "routed:" + originHost + ":" + subjectID
}

// surfaceFirstAttended probes every peer's presence concurrently (bounded by
// maxConcurrentHosts) and surfaces note on the first attended peer in registry
// order, deciding each prefix as its probes land so one slow probe never delays
// an already-decided attended peer. An unreachable, erroring, or unattended
// peer is skipped; a failed surface falls over to the next attended peer. It
// returns ("", nil) when no peer is attended, and the joined surface errors
// when every attended peer's surface failed.
func (r Router) surfaceFirstAttended(ctx context.Context, reg *hostregistry.Registry, session string, note interaction.NotificationPayload) (string, error) {
	n := len(reg.Hosts)
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type probed struct {
		i        int
		attended bool
	}
	results := make(chan probed, n)
	sem := make(chan struct{}, maxConcurrentHosts)
	attemptTimeout := r.peerAttemptTimeout()
	for i, h := range reg.Hosts {
		go func(i int, h string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			pctx, pcancel := context.WithTimeout(probeCtx, attemptTimeout)
			defer pcancel()
			p, err := probePeerPresence(pctx, fanTarget{host: h, runner: r.Dial(h)})
			results <- probed{i: i, attended: err == nil && p.Attended}
		}(i, h)
	}
	decided := make([]bool, n)
	attended := make([]bool, n)
	var errs []error
	next := 0
	for range n {
		p := <-results
		decided[p.i], attended[p.i] = true, p.attended
		for next < n && decided[next] {
			if attended[next] {
				sctx, scancel := context.WithTimeout(ctx, attemptTimeout)
				err := r.surface(sctx, reg.Hosts[next], session, note)
				scancel()
				if err == nil {
					return reg.Hosts[next], nil
				}
				errs = append(errs, err)
			}
			next++
		}
	}
	return "", errors.Join(errs...)
}

// probePeerPresence runs `cc-runtime rpc mesh.presence` on the peer and decodes
// its attendance report.
func probePeerPresence(ctx context.Context, t fanTarget) (Presence, error) {
	reply, _, err := meshRPC(ctx, t, OpPresence, "", struct{}{})
	if err != nil {
		return Presence{}, err
	}
	if !reply.OK {
		return Presence{}, errors.New(reply.Error)
	}
	var p Presence
	if err := json.Unmarshal(reply.Body, &p); err != nil {
		return Presence{}, err
	}
	return p, nil
}

// surface delivers note to the peer over `cc-runtime rpc interaction.notify`,
// stamping session as the peer-side window identity and returning every failure
// so a surface that does not land is never silently dropped. The notify is not
// idempotent, so it rides the single-dial leg: a failover retry would re-append
// a notification the peer already recorded.
func (r Router) surface(ctx context.Context, target, session string, note interaction.NotificationPayload) error {
	reply, _, err := meshRPCOnce(ctx, fanTarget{host: target, runner: r.Dial(target)}, interaction.OpNotify, session, note)
	if err != nil {
		return fmt.Errorf("surface on %s: %w", hostregistry.HostNode(target), err)
	}
	if !reply.OK {
		return fmt.Errorf("surface on %s: %s", hostregistry.HostNode(target), reply.Error)
	}
	return nil
}
