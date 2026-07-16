package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"

	"github.com/yasyf/cc-runtime/interaction"
)

// RoutingEnabled reports whether presence routing is active for reg: it needs at
// least one registered peer and must not be switched off (`mesh route off`).
func RoutingEnabled(reg *Registry) bool {
	return len(reg.Hosts) > 0 && !reg.RouteOff
}

// RoutedNotification composes the surface a peer receives for an interaction that
// fired on an unattended origin: an origin-tagged notification naming the machine,
// the subject, and the question header, so the human at the peer knows where to
// look. It carries no bearer token — answering happens against the origin over the
// mesh answer path — and its urgency is deliberately empty so the receiving peer
// never treats it as a routable event and bounces it onward.
func RoutedNotification(originHost, subjectID, header string) interaction.NotificationPayload {
	node := HostNode(originHost)
	msg := fmt.Sprintf("[%s] needs you: %s (subject %s on %s)", node, header, subjectID, node)
	return interaction.NotificationPayload{Message: msg}
}

// Router surfaces an interaction on an attended peer when this host is unattended.
// It is additive to the always-on push lanes: routing only adds a peer-machine
// surface, never replaces the phones a push reaches wherever they are.
type Router struct {
	Store Store
	Local Runner
	Dial  func(target string) Runner
	GOOS  string
}

// NewRouter builds a Router rooted at the app mesh store, probing this host over
// local and dialing peers over ssh.
func NewRouter(store Store, local Runner, dial func(target string) Runner) Router {
	return Router{Store: store, Local: local, Dial: dial, GOOS: runtime.GOOS}
}

// Route surfaces the interaction (subjectID, header) on the first attended peer
// when this host is unattended and routing is enabled. It returns the peer it
// surfaced on, or "" when it fell back: routing off, no peers, this host attended
// (the human is already here), or no peer attended anywhere. The registry is read
// fresh each call so `host add`/`rm` and `route off` take effect without a daemon
// restart. Routing is best-effort — the caller has already fired the push lanes —
// so an error is returned for logging, never to block delivery.
func (r Router) Route(ctx context.Context, subjectID, header string) (string, error) {
	reg, err := r.Store.Load()
	if err != nil {
		return "", err
	}
	if !RoutingEnabled(reg) {
		return "", nil
	}
	local, err := DetectPresence(ctx, r.GOOS, r.Local)
	if err != nil {
		return "", err
	}
	if local.Attended {
		return "", nil
	}
	target, ok := r.firstAttendedPeer(ctx, reg)
	if !ok {
		return "", nil
	}
	if err := r.surface(ctx, target, RoutedNotification(reg.Self, subjectID, header)); err != nil {
		return "", err
	}
	return target, nil
}

// firstAttendedPeer probes every peer's presence concurrently and returns the
// first attended peer in registry order. A peer that is unreachable or errors is
// treated as not attended and skipped, so one dead peer never blocks routing to a
// live one.
func (r Router) firstAttendedPeer(ctx context.Context, reg *Registry) (string, bool) {
	attended := make([]bool, len(reg.Hosts))
	fanOut(len(reg.Hosts), func(i int) {
		p, err := probePeerPresence(ctx, r.Dial(reg.Hosts[i]))
		attended[i] = err == nil && p.Attended
	})
	for i, ok := range attended {
		if ok {
			return reg.Hosts[i], true
		}
	}
	return "", false
}

// probePeerPresence runs `cc-runtime rpc mesh.presence` on the peer and decodes
// its attendance report.
func probePeerPresence(ctx context.Context, runner Runner) (Presence, error) {
	reply, _, err := meshRPC(ctx, runner, OpPresence, struct{}{})
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
// returning every failure so a surface that does not land is never silently
// dropped.
func (r Router) surface(ctx context.Context, target string, note interaction.NotificationPayload) error {
	reply, _, err := meshRPC(ctx, r.Dial(target), interaction.OpNotify, note)
	if err != nil {
		return fmt.Errorf("surface on %s: %w", HostNode(target), err)
	}
	if !reply.OK {
		return fmt.Errorf("surface on %s: %s", HostNode(target), reply.Error)
	}
	return nil
}
