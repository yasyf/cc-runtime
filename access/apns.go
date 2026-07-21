package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
	"github.com/yasyf/cc-interact/store"
	"golang.org/x/net/http2"
)

// Event types appended to the push subject's log for the APNs device registry.
const (
	EventPushDeviceRegister   = "push.device_register"
	EventPushDeviceUnregister = "push.device_unregister"
)

// APNs delivery hosts; Sandbox in APNSConfig picks the second.
const (
	apnsHost        = "https://api.push.apple.com"
	apnsSandboxHost = "https://api.sandbox.push.apple.com"
)

// Registration bounds, mirroring the Web Push subscription bounds: the body
// cap rejects oversized frames before decoding, the token bounds cover
// Apple's variable-length hex tokens, and the set cap bounds the goroutines +
// outbound sockets every fan-out spawns per stored row.
const (
	maxDeviceRegistrationBytes = 1 << 10
	minDeviceTokenChars        = 64
	maxDeviceTokenChars        = 200
	maxDeviceTokens            = 32
)

// devicePlatform is the only platform the registry accepts today.
const devicePlatform = "ios"

// ErrDeviceTokenLimit rejects a registration that would grow the stored
// device-token set beyond maxDeviceTokens.
var ErrDeviceTokenLimit = fmt.Errorf("device token limit (%d) reached", maxDeviceTokens)

// APNSStoreSchema projects the current device-token set out of the push event log,
// keyed by token (the dedupe key); the log stays the durable record. It layers
// on PushStoreSchema, which inserts the well-known push subject the rows reference.
var APNSStoreSchema = store.Schema{DDL: `
CREATE TABLE apns_device_tokens (
  token      TEXT PRIMARY KEY,
  platform   TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`}

// deviceRegistration is the wire frame a client POSTs to register its APNs
// device token.
type deviceRegistration struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

// apnsEventFrame is the self-contained log payload for device register and
// unregister events, mirroring pushEventFrame.
type apnsEventFrame struct {
	Type     string `json:"type"`
	Token    string `json:"token"`
	Platform string `json:"platform,omitempty"`
}

// apnsBody is the alert APNs delivers to a device. It carries the compact
// push frame under "payload" and never the pair bearer token.
type apnsBody struct {
	APS     apnsAPS   `json:"aps"`
	Payload apnsFrame `json:"payload"`
}

type apnsAPS struct {
	Alert    apnsAlert `json:"alert"`
	Sound    string    `json:"sound"`
	ThreadID string    `json:"thread-id"`
}

type apnsAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body"`
}

type apnsFrame struct {
	Type    string `json:"type"`
	Subject string `json:"subject"`
	Urgency string `json:"urgency,omitempty"`
}

// httpDoer is the injected network boundary an APNSSender delivers through.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// APNSSender fans frames out to every registered device token over HTTP/2,
// authenticating with the cached ES256 provider token. The HTTP client is the
// injected network boundary; signing, registration, and pruning are real. mu
// serializes registration for the same reason as PushSender.mu: the cap must
// refuse before the durable append, so only in-process serialization keeps
// concurrent registrations at the cap from overshooting.
type APNSSender struct {
	auth   *apnsAuth
	topic  string
	host   string
	db     *sql.DB
	append daemon.AppendFunc
	client httpDoer
	now    func() time.Time
	mu     sync.Mutex
}

// NewAPNSSender builds a sender from an enabled config, loading the .p8 key
// it references — a configured lane with a broken key fails loudly. Delivery
// rides a dedicated HTTP/2 transport (APNs requires h2), bounded per fan-out
// by the caller's context.
func NewAPNSSender(cfg APNSConfig, db *sql.DB, append daemon.AppendFunc) (*APNSSender, error) {
	key, err := LoadAPNSKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	host := apnsHost
	if cfg.Sandbox {
		host = apnsSandboxHost
	}
	return &APNSSender{
		auth:   &apnsAuth{key: key, keyID: cfg.KeyID, teamID: cfg.TeamID},
		topic:  cfg.BundleID,
		host:   host,
		db:     db,
		append: append,
		client: &http.Client{Transport: &http2.Transport{}},
		now:    time.Now,
	}, nil
}

// Register durably stores a device token: append the register event (deduped
// by token), then project the row — append-first, like every interaction
// write. A registration that would grow the stored set beyond maxDeviceTokens
// is refused (ErrDeviceTokenLimit); re-registering a stored token always
// passes and refreshes created_at, so a delayed invalidation can be ordered
// against it.
func (a *APNSSender) Register(ctx context.Context, token, platform string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var others int
	if err := a.db.QueryRowContext(ctx,
		`SELECT count(*) FROM apns_device_tokens WHERE token<>?`, token).Scan(&others); err != nil {
		return fmt.Errorf("count device tokens: %w", err)
	}
	if others >= maxDeviceTokens {
		return ErrDeviceTokenLimit
	}
	frame, err := json.Marshal(apnsEventFrame{Type: EventPushDeviceRegister, Token: token, Platform: platform})
	if err != nil {
		return err
	}
	if _, err := a.append(ctx, &event.Event{
		SubjectID: PushSubjectID, Origin: event.OriginHuman, Type: EventPushDeviceRegister,
		Payload: frame, DedupKey: "device:" + token,
	}); err != nil {
		return fmt.Errorf("append device registration: %w", err)
	}
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO apns_device_tokens(token, platform, created_at) VALUES(?,?,?)
		 ON CONFLICT(token) DO UPDATE SET platform=excluded.platform, created_at=excluded.created_at`,
		token, platform, a.now().UnixMilli()); err != nil {
		return fmt.Errorf("project device token: %w", err)
	}
	return nil
}

// Fanout delivers payload to every registered device token in parallel. A
// token APNs reports unregistered (410, or 400 BadDeviceToken) is pruned —
// its row deleted behind a durable unregister event — and does not count as
// a failure; any other non-2xx or transport error does.
func (a *APNSSender) Fanout(ctx context.Context, payload PushPayload) error {
	tokens, err := listDeviceTokens(ctx, a.db)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}
	body, err := json.Marshal(apnsBody{
		APS: apnsAPS{
			Alert:    apnsAlert{Title: payload.Title, Body: payload.Body},
			Sound:    "default",
			ThreadID: payload.Subject,
		},
		Payload: apnsFrame{Type: payload.Type, Subject: payload.Subject, Urgency: payload.Urgency},
	})
	if err != nil {
		return err
	}
	bearer, err := a.auth.bearer(a.now())
	if err != nil {
		return err
	}
	priority := apnsPriority(payload.Urgency)
	expiration := strconv.FormatInt(a.now().Add(pushTTL*time.Second).Unix(), 10)
	errs := make([]error, len(tokens))
	var wg sync.WaitGroup
	for i, token := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = a.send(ctx, body, token, bearer, priority, expiration)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// send POSTs one alert to APNs. The provider token authenticates the daemon;
// the pair bearer token never rides the request.
func (a *APNSSender) send(ctx context.Context, body []byte, token, bearer, priority, expiration string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.host+"/3/device/"+token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "bearer "+bearer)
	req.Header.Set("Apns-Topic", a.topic)
	req.Header.Set("Apns-Push-Type", "alert")
	req.Header.Set("Apns-Priority", priority)
	req.Header.Set("Apns-Expiration", expiration)
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("apns %s: %s", redactToken(token), scrubbedCause(err, token))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	reason, invalidatedAt := apnsFailure(resp.Body)
	if resp.StatusCode == http.StatusGone || (resp.StatusCode == http.StatusBadRequest && reason == "BadDeviceToken") {
		return a.pruneInvalidated(ctx, token, invalidatedAt)
	}
	return fmt.Errorf("apns %s: status %d reason %q", redactToken(token), resp.StatusCode, reason)
}

// redactToken abbreviates a device token for error strings: a registered token
// is a delivery credential, so errors (and the logs they land in) carry only a
// short prefix — and a token too short to spare one is elided entirely.
func redactToken(token string) string {
	const keep = 8
	if len(token) <= keep {
		return "…"
	}
	return token[:keep] + "…"
}

// scrubbedCause renders an error's message with every url.Error layer peeled
// (each embeds the request URL, whose path is the token) and every remaining
// occurrence of the full token redacted. The cause chain is deliberately cut:
// a transport or append error can carry the URL or event payload verbatim.
func scrubbedCause(err error, token string) string {
	for {
		var ue *url.Error
		if !errors.As(err, &ue) {
			break
		}
		err = ue.Err
	}
	return strings.ReplaceAll(err.Error(), token, redactToken(token))
}

// pruneInvalidated retires token unless it was re-registered after APNs
// invalidated it: a 410's invalidation timestamp (ms) older than the row's
// created_at marks the response as stale against a fresh grant, so it is
// dropped. Without a timestamp (400 BadDeviceToken) the token always prunes.
func (a *APNSSender) pruneInvalidated(ctx context.Context, token string, invalidatedAt int64) error {
	if invalidatedAt > 0 {
		var createdAt int64
		err := a.db.QueryRowContext(ctx,
			`SELECT created_at FROM apns_device_tokens WHERE token=?`, token).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read device token %s: %s", redactToken(token), scrubbedCause(err, token))
		}
		if createdAt > invalidatedAt {
			return nil
		}
	}
	return pruneDeviceToken(ctx, a.db, a.append, token)
}

// PurgeDeviceTokens retires every stored APNs registration through
// pruneDeviceToken, so each revocation leaves a durable unregister event. It
// mirrors PushSender.purgeAll: a device registration is a durable delivery
// grant, and a revoked access surface or a disabled APNs lane holds none.
func PurgeDeviceTokens(ctx context.Context, db *sql.DB, append daemon.AppendFunc) error {
	tokens, err := listDeviceTokens(ctx, db)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if err := pruneDeviceToken(ctx, db, append, token); err != nil {
			return err
		}
	}
	return nil
}

// pruneDeviceToken retires an unregistered device token: durable unregister
// event first, then the projection delete, mirroring the append-then-project
// write order.
func pruneDeviceToken(ctx context.Context, db *sql.DB, append daemon.AppendFunc, token string) error {
	frame, err := json.Marshal(apnsEventFrame{Type: EventPushDeviceUnregister, Token: token})
	if err != nil {
		return err
	}
	if _, err := append(ctx, &event.Event{
		SubjectID: PushSubjectID, Origin: event.OriginSystem, Type: EventPushDeviceUnregister, Payload: frame,
	}); err != nil {
		// The event payload carries the full token; an AppendFunc error can echo it.
		return fmt.Errorf("append device unregister %s: %s", redactToken(token), scrubbedCause(err, token))
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM apns_device_tokens WHERE token=?`, token); err != nil {
		return fmt.Errorf("prune device token %s: %s", redactToken(token), scrubbedCause(err, token))
	}
	return nil
}

// listDeviceTokens loads the projected device-token set, token-ordered.
func listDeviceTokens(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT token FROM apns_device_tokens ORDER BY token`)
	if err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan device token: %w", err)
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

// apnsPriority maps the RFC 8030 urgency onto Apple's scale: 10 (immediate)
// for high, 5 (power-considerate) otherwise.
func apnsPriority(urgency string) string {
	if urgency == PushUrgencyHigh {
		return "10"
	}
	return "5"
}

// apnsFailure decodes the error reason and, on a 410, the invalidation
// timestamp (ms since epoch) APNs returns on a non-200 response; a missing or
// malformed body yields zero values.
func apnsFailure(r io.Reader) (reason string, timestamp int64) {
	var body struct {
		Reason    string `json:"reason"`
		Timestamp int64  `json:"timestamp"`
	}
	_ = json.NewDecoder(io.LimitReader(r, 4<<10)).Decode(&body)
	return body.Reason, body.Timestamp
}

// MountAPNS mounts device-token registration on the daemon's auth-guarded
// mux, mirroring MountPush — but it re-checks the pair bearer itself: the
// daemon's auth layer admits tokenless loopback peers, and a registration is
// a durable remote delivery grant no local process may mint without the
// token. With no token minted yet, every registration is refused.
func MountAPNS(mux *http.ServeMux, sender *APNSSender, token string) {
	mux.HandleFunc("POST /api/push/device-tokens", func(w http.ResponseWriter, r *http.Request) {
		if !bearerAuthorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxDeviceRegistrationBytes)
		var reg deviceRegistration
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, fmt.Sprintf("registration exceeds %d bytes", maxDeviceRegistrationBytes), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad registration: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateDeviceRegistration(reg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := sender.Register(r.Context(), reg.Token, reg.Platform); err != nil {
			if errors.Is(err, ErrDeviceTokenLimit) {
				http.Error(w, err.Error(), http.StatusTooManyRequests)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}

// bearerAuthorized reports whether r presents the pair bearer token, comparing
// SHA-256 digests in constant time so a length mismatch cannot be timed.
func bearerAuthorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	candidate, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	c := sha256.Sum256([]byte(candidate))
	t := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(c[:], t[:]) == 1
}

// validateDeviceRegistration rejects a frame the sender could never deliver
// to: the platform must be ios, and the token must be even-length lowercase
// hex within the length bounds.
func validateDeviceRegistration(reg deviceRegistration) error {
	if reg.Platform != devicePlatform {
		return fmt.Errorf("device platform must be %q", devicePlatform)
	}
	if len(reg.Token) < minDeviceTokenChars || len(reg.Token) > maxDeviceTokenChars {
		return fmt.Errorf("device token must be %d-%d hex characters", minDeviceTokenChars, maxDeviceTokenChars)
	}
	if len(reg.Token)%2 != 0 {
		return errors.New("device token must be an even number of hex characters")
	}
	for _, c := range []byte(reg.Token) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("device token must be lowercase hex")
		}
	}
	return nil
}
