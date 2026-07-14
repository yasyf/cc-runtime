package access

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
)

// PushSubjectID is the well-known subject keying push-plane events (subscribe,
// unsubscribe) on the daemon's event log. PushMigrate inserts its subjects row;
// its scope and status sit outside every real window's, so it is never
// resolved, adopted, or listed as an interaction subject.
const PushSubjectID = "push"

// Event types appended to the push subject's log.
const (
	EventPushSubscribe   = "push.subscribe"
	EventPushUnsubscribe = "push.unsubscribe"
)

// PushUrgencyHigh is the RFC 8030 urgency for frames that must reach a device
// even on low battery (an open question blocking the agent).
const PushUrgencyHigh = string(webpush.UrgencyHigh)

// pushTTL is how long (seconds) a push service holds an undelivered frame
// before dropping it.
const pushTTL = 3600

// pushSubscriber is the VAPID sub claim: where a push service can reach the
// operator of this application server.
const pushSubscriber = "https://github.com/yasyf/cc-runtime"

// pushSchema projects the current subscription set out of the push event log,
// keyed by endpoint (the dedupe key); the log stays the durable record. The
// well-known push subject the log rows reference is inserted by PushMigrate.
const pushSchema = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint     TEXT PRIMARY KEY,
  subscription TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);
`

// PushMigrate applies the push-plane schema on top of cc-interact's core and
// inserts the well-known push subject the event log's foreign key requires.
func PushMigrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, pushSchema); err != nil {
		return fmt.Errorf("apply push schema: %w", err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO subjects(id, slug, scope, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		PushSubjectID, PushSubjectID, PushSubjectID, PushSubjectID, now, now); err != nil {
		return fmt.Errorf("insert push subject: %w", err)
	}
	return nil
}

// PushPayload is the compact frame delivered (encrypted) to every push
// endpoint: what happened (the wire event type), where (the subject), and the
// text a service worker renders. It never carries the pair token — a push
// service sits outside the daemon's trust boundary.
type PushPayload struct {
	Type    string `json:"type"`
	Subject string `json:"subject"`
	Title   string `json:"title,omitempty"`
	Body    string `json:"body"`
	Urgency string `json:"urgency,omitempty"`
}

// pushEventFrame is the self-contained log payload for subscribe/unsubscribe
// events. The type discriminator lives inside the payload because the SSE
// plane streams only payloads, mirroring the interaction wire convention.
type pushEventFrame struct {
	Type     string        `json:"type"`
	Endpoint string        `json:"endpoint"`
	Keys     *webpush.Keys `json:"keys,omitempty"`
}

// PushSender fans frames out to every registered Web Push subscription. The
// HTTP client is the injected network boundary; everything else — VAPID
// signing, payload encryption, registration, pruning — is real.
type PushSender struct {
	keys   VAPIDKeys
	db     *sql.DB
	append daemon.AppendFunc
	client webpush.HTTPClient
}

// NewPushSender returns a sender delivering over the default HTTP client;
// every request is bounded by the fan-out context.
func NewPushSender(keys VAPIDKeys, db *sql.DB, append daemon.AppendFunc) *PushSender {
	return &PushSender{keys: keys, db: db, append: append, client: &http.Client{}}
}

// Subscribe durably registers a push subscription: append the subscribe event
// (deduped by endpoint), then project the row — append-first, like every
// interaction write.
func (p *PushSender) Subscribe(ctx context.Context, sub webpush.Subscription) error {
	frame, err := json.Marshal(pushEventFrame{Type: EventPushSubscribe, Endpoint: sub.Endpoint, Keys: &sub.Keys})
	if err != nil {
		return err
	}
	if _, err := p.append(ctx, &event.Event{
		SubjectID: PushSubjectID, Origin: event.OriginHuman, Type: EventPushSubscribe,
		Payload: frame, DedupKey: "sub:" + sub.Endpoint,
	}); err != nil {
		return fmt.Errorf("append push subscription: %w", err)
	}
	subJSON, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO push_subscriptions(endpoint, subscription, created_at) VALUES(?,?,?)
		 ON CONFLICT(endpoint) DO UPDATE SET subscription=excluded.subscription`,
		sub.Endpoint, string(subJSON), time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("project push subscription: %w", err)
	}
	return nil
}

// Fanout delivers payload to every registered endpoint in parallel. A gone
// endpoint (404/410) is pruned — its row deleted behind a durable unsubscribe
// event — and does not count as a failure; any other non-2xx or transport
// error does.
func (p *PushSender) Fanout(ctx context.Context, payload PushPayload) error {
	subs, err := p.subscriptions(ctx)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	errs := make([]error, len(subs))
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = p.send(ctx, msg, sub, payload.Urgency)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// send encrypts msg against one subscription's keys and POSTs it, signed with
// the VAPID keypair. webpush pads the message in place, so each send gets its
// own copy — parallel sends must not share a backing array.
func (p *PushSender) send(ctx context.Context, msg []byte, sub webpush.Subscription, urgency string) error {
	resp, err := webpush.SendNotificationWithContext(ctx, bytes.Clone(msg), &sub, &webpush.Options{
		HTTPClient:      p.client,
		Subscriber:      pushSubscriber,
		TTL:             pushTTL,
		Urgency:         webpush.Urgency(urgency),
		VAPIDPublicKey:  p.keys.Public,
		VAPIDPrivateKey: p.keys.Private,
	})
	if err != nil {
		return fmt.Errorf("push %s: %w", sub.Endpoint, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return p.prune(ctx, sub.Endpoint)
	case resp.StatusCode >= http.StatusMultipleChoices:
		return fmt.Errorf("push %s: status %d", sub.Endpoint, resp.StatusCode)
	}
	return nil
}

// prune retires a gone endpoint: durable unsubscribe event first, then the
// projection delete, mirroring the append-then-project write order.
func (p *PushSender) prune(ctx context.Context, endpoint string) error {
	frame, err := json.Marshal(pushEventFrame{Type: EventPushUnsubscribe, Endpoint: endpoint})
	if err != nil {
		return err
	}
	if _, err := p.append(ctx, &event.Event{
		SubjectID: PushSubjectID, Origin: event.OriginSystem, Type: EventPushUnsubscribe, Payload: frame,
	}); err != nil {
		return fmt.Errorf("append push unsubscribe %s: %w", endpoint, err)
	}
	if _, err := p.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint); err != nil {
		return fmt.Errorf("prune push subscription %s: %w", endpoint, err)
	}
	return nil
}

// subscriptions loads the projected subscription set, endpoint-ordered.
func (p *PushSender) subscriptions(ctx context.Context) ([]webpush.Subscription, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT subscription FROM push_subscriptions ORDER BY endpoint`)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()
	var out []webpush.Subscription
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		var sub webpush.Subscription
		if err := json.Unmarshal([]byte(raw), &sub); err != nil {
			return nil, fmt.Errorf("parse push subscription: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// MountPush mounts the Web Push surface on the daemon's auth-guarded mux: the
// VAPID public key a client subscribes with, and subscription registration.
// Endpoint-over-pair-payload rationale: cc-notes 3d24919a.
func MountPush(mux *http.ServeMux, sender *PushSender) {
	mux.HandleFunc("GET /api/push/vapid-key", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"key": sender.keys.Public})
	})
	mux.HandleFunc("POST /api/push/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		var sub webpush.Subscription
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			http.Error(w, "bad subscription: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateSubscription(sub); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := sender.Subscribe(r.Context(), sub); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}

// validateSubscription rejects a frame the sender could never deliver to: the
// endpoint must be an absolute https URL (the daemon POSTs to it) and both
// client keys must be present (the payload is encrypted against them).
func validateSubscription(sub webpush.Subscription) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("subscription endpoint must be an absolute https URL")
	}
	if sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("subscription keys p256dh and auth are required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
