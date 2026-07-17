package access

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/yasyf/cc-interact/store"
)

type recordedPush struct {
	url    string
	header http.Header
	body   []byte
}

// fakePushClient is the mocked network boundary: it records every outbound
// webpush request and answers with the status configured per endpoint.
type fakePushClient struct {
	mu       sync.Mutex
	status   map[string]int
	requests []recordedPush
}

func (f *fakePushClient) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code, ok := f.status[req.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected push to %s", req.URL)
	}
	f.requests = append(f.requests, recordedPush{url: req.URL.String(), header: req.Header.Clone(), body: body})
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

func (f *fakePushClient) recorded() []recordedPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedPush(nil), f.requests...)
}

type pushFixture struct {
	t      *testing.T
	store  *store.Store
	sender *PushSender
	client *fakePushClient
	mux    *http.ServeMux
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"), func(ctx context.Context, db *sql.DB) error {
		if err := PushMigrate(ctx, db); err != nil {
			return err
		}
		return APNSMigrate(ctx, db)
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	keys, err := Store{Dir: t.TempDir()}.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID: %v", err)
	}
	client := &fakePushClient{status: map[string]int{}}
	sender := &PushSender{keys: keys, db: st.DB(), append: st.AppendEvent, client: client}
	mux := http.NewServeMux()
	MountPush(mux, sender)
	return &pushFixture{t: t, store: st, sender: sender, client: client, mux: mux}
}

func (f *pushFixture) post(body string) *httptest.ResponseRecorder {
	return f.postTyped(body, "application/json")
}

func (f *pushFixture) postTyped(body, contentType string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/push/subscriptions", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *pushFixture) subscribe(sub webpush.Subscription) {
	f.t.Helper()
	raw, err := json.Marshal(sub)
	if err != nil {
		f.t.Fatalf("marshal subscription: %v", err)
	}
	if rec := f.post(string(raw)); rec.Code != http.StatusOK {
		f.t.Fatalf("subscribe %s = %d %s, want 200", sub.Endpoint, rec.Code, rec.Body)
	}
}

func (f *pushFixture) endpoints() []string {
	f.t.Helper()
	rows, err := f.store.DB().Query(`SELECT endpoint FROM push_subscriptions ORDER BY endpoint`)
	if err != nil {
		f.t.Fatalf("query push_subscriptions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ep string
		if err := rows.Scan(&ep); err != nil {
			f.t.Fatalf("scan endpoint: %v", err)
		}
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("rows: %v", err)
	}
	return out
}

// registerDevice seeds an APNs registration on the same store, so reconcile
// tests cover both grant kinds.
func (f *pushFixture) registerDevice(token string) {
	f.t.Helper()
	apns := &APNSSender{db: f.store.DB(), append: f.store.AppendEvent, now: time.Now}
	if err := apns.Register(context.Background(), token, "ios"); err != nil {
		f.t.Fatalf("Register device token: %v", err)
	}
}

func (f *pushFixture) deviceTokens() []string {
	f.t.Helper()
	rows, err := f.store.DB().Query(`SELECT token FROM apns_device_tokens ORDER BY token`)
	if err != nil {
		f.t.Fatalf("query apns_device_tokens: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			f.t.Fatalf("scan token: %v", err)
		}
		out = append(out, token)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("rows: %v", err)
	}
	return out
}

func (f *pushFixture) events() []pushEventFrame {
	f.t.Helper()
	evs, err := f.store.EventsSince(context.Background(), PushSubjectID, 0, "")
	if err != nil {
		f.t.Fatalf("EventsSince: %v", err)
	}
	out := make([]pushEventFrame, len(evs))
	for i, ev := range evs {
		if err := json.Unmarshal(ev.Payload, &out[i]); err != nil {
			f.t.Fatalf("unmarshal event payload %q: %v", ev.Payload, err)
		}
		if out[i].Type != ev.Type {
			f.t.Fatalf("event %d payload type %q disagrees with event type %q", i, out[i].Type, ev.Type)
		}
	}
	return out
}

// testSubscription mints a real browser-shaped subscription: a P-256 client
// keypair plus a 16-byte auth secret, so the sender's payload encryption runs
// against genuine key material.
func testSubscription(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client keypair: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth),
	}}
}

func TestSubscribeRoundTripDedupesByEndpoint(t *testing.T) {
	f := newPushFixture(t)
	sub := testSubscription(t, "https://push.example/reg/1")

	raw, _ := json.Marshal(sub)
	rec := f.post(string(raw))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("subscribe = %d %q, want 200 {\"ok\":true}", rec.Code, rec.Body)
	}

	if got := f.endpoints(); len(got) != 1 || got[0] != sub.Endpoint {
		t.Fatalf("projected endpoints = %v, want [%s]", got, sub.Endpoint)
	}
	events := f.events()
	if len(events) != 1 || events[0].Type != EventPushSubscribe || events[0].Endpoint != sub.Endpoint {
		t.Fatalf("events = %+v, want one %s for %s", events, EventPushSubscribe, sub.Endpoint)
	}
	if events[0].Keys == nil || events[0].Keys.P256dh != sub.Keys.P256dh || events[0].Keys.Auth != sub.Keys.Auth {
		t.Fatalf("subscribe event keys = %+v, want the client's %+v", events[0].Keys, sub.Keys)
	}

	// A repeated registration is an idempotent no-op: one row, one event.
	f.subscribe(sub)
	if got := f.endpoints(); len(got) != 1 {
		t.Fatalf("endpoints after re-subscribe = %v, want still one", got)
	}
	if events := f.events(); len(events) != 1 {
		t.Fatalf("events after re-subscribe = %+v, want still one (deduped by endpoint)", events)
	}
}

// TestSubscribeRejectsNonJSONContentType pins the CSRF guard: a cross-site
// "simple" POST (text/plain, form encodings — no CORS preflight needed) must
// never register a push endpoint, even when peer trust admits the request.
func TestSubscribeRejectsNonJSONContentType(t *testing.T) {
	f := newPushFixture(t)
	raw, _ := json.Marshal(testSubscription(t, "https://push.example/reg/csrf"))
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		if rec := f.postTyped(string(raw), ct); rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("subscribe with Content-Type %q = %d, want 415", ct, rec.Code)
		}
	}
	if got := f.endpoints(); len(got) != 0 {
		t.Fatalf("a non-JSON POST registered endpoints: %v", got)
	}
}

func TestSubscribeRejectsUndeliverableSubscriptions(t *testing.T) {
	valid := testSubscription(t, "https://push.example/reg/ok")
	noP256dh := valid
	noP256dh.Keys.P256dh = ""
	noAuth := valid
	noAuth.Keys.Auth = ""
	plainHTTP := valid
	plainHTTP.Endpoint = "http://push.example/reg/insecure"
	relative := valid
	relative.Endpoint = "/reg/relative"

	marshal := func(s webpush.Subscription) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	for _, tc := range []struct {
		id   string
		body string
	}{
		{id: "malformed json", body: "{"},
		{id: "http endpoint", body: marshal(plainHTTP)},
		{id: "relative endpoint", body: marshal(relative)},
		{id: "missing p256dh", body: marshal(noP256dh)},
		{id: "missing auth", body: marshal(noAuth)},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newPushFixture(t)
			if rec := f.post(tc.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("subscribe = %d, want 400", rec.Code)
			}
			if got := f.endpoints(); len(got) != 0 {
				t.Fatalf("projected endpoints = %v, want none", got)
			}
			if events := f.events(); len(events) != 0 {
				t.Fatalf("events = %+v, want none", events)
			}
		})
	}
}

func TestSubscribeRejectsOversizedRegistrations(t *testing.T) {
	valid := testSubscription(t, "https://push.example/reg/ok")
	longEndpoint := valid
	longEndpoint.Endpoint = "https://push.example/" + strings.Repeat("x", maxEndpointBytes)
	longKey := valid
	longKey.Keys.P256dh = strings.Repeat("A", maxKeyBytes+1)

	marshal := func(s webpush.Subscription) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	for _, tc := range []struct {
		id       string
		body     string
		wantCode int
	}{
		{id: "oversized body", body: `{"pad":"` + strings.Repeat("x", maxSubscriptionBytes) + `"}`, wantCode: http.StatusRequestEntityTooLarge},
		{id: "oversized endpoint", body: marshal(longEndpoint), wantCode: http.StatusBadRequest},
		{id: "oversized key", body: marshal(longKey), wantCode: http.StatusBadRequest},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newPushFixture(t)
			if rec := f.post(tc.body); rec.Code != tc.wantCode {
				t.Fatalf("subscribe = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := f.endpoints(); len(got) != 0 {
				t.Fatalf("projected endpoints = %v, want none", got)
			}
		})
	}
}

func TestSubscribeCapsStoredSet(t *testing.T) {
	f := newPushFixture(t)
	for i := range maxSubscriptions {
		f.subscribe(testSubscription(t, fmt.Sprintf("https://push.example/reg/%03d", i)))
	}

	over := testSubscription(t, "https://push.example/reg/overflow")
	raw, _ := json.Marshal(over)
	if rec := f.post(string(raw)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("subscribe over cap = %d %s, want 429", rec.Code, rec.Body)
	}
	if got := f.endpoints(); len(got) != maxSubscriptions {
		t.Fatalf("stored subscriptions = %d, want %d", len(got), maxSubscriptions)
	}

	// Re-registering a stored endpoint still passes at the cap.
	f.subscribe(testSubscription(t, "https://push.example/reg/000"))
	if got := f.endpoints(); len(got) != maxSubscriptions {
		t.Fatalf("stored subscriptions after re-register = %d, want %d", len(got), maxSubscriptions)
	}
}

func TestReconcileGrantsRevokesOnSurfaceChange(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		id                    string
		firstToken, nextToken string
		firstBind, nextBind   string
		wantPurged            bool
	}{
		{id: "token rotation purges", firstToken: "tok-a", nextToken: "tok-b", firstBind: "0.0.0.0", nextBind: "0.0.0.0", wantPurged: true},
		{id: "remote access off purges", firstToken: "tok-a", nextToken: "tok-a", firstBind: "0.0.0.0", nextBind: "127.0.0.1", wantPurged: true},
		{id: "same surface keeps grants", firstToken: "tok-a", nextToken: "tok-a", firstBind: "0.0.0.0", nextBind: "0.0.0.0", wantPurged: false},
		{id: "loopback to lan keeps grants", firstToken: "tok-a", nextToken: "tok-a", firstBind: "127.0.0.1", nextBind: "0.0.0.0", wantPurged: false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			f := newPushFixture(t)
			if err := f.sender.ReconcileGrants(ctx, tt.firstToken, tt.firstBind); err != nil {
				t.Fatalf("ReconcileGrants (first boot): %v", err)
			}
			sub := testSubscription(t, "https://push.example/reg/1")
			f.subscribe(sub)
			f.registerDevice(deviceToken(0))

			if err := f.sender.ReconcileGrants(ctx, tt.nextToken, tt.nextBind); err != nil {
				t.Fatalf("ReconcileGrants (next boot): %v", err)
			}

			got := f.endpoints()
			if tt.wantPurged {
				if len(got) != 0 {
					t.Fatalf("stored subscriptions = %v, want purged", got)
				}
				if devices := f.deviceTokens(); len(devices) != 0 {
					t.Fatalf("stored device tokens = %v, want purged", devices)
				}
				events := f.events()
				unsub, unreg := events[len(events)-2], events[len(events)-1]
				if unsub.Type != EventPushUnsubscribe || unsub.Endpoint != sub.Endpoint {
					t.Fatalf("event = %+v, want %s for %s", unsub, EventPushUnsubscribe, sub.Endpoint)
				}
				if unreg.Type != EventPushDeviceUnregister {
					t.Fatalf("last event = %+v, want %s", unreg, EventPushDeviceUnregister)
				}
				return
			}
			if len(got) != 1 || got[0] != sub.Endpoint {
				t.Fatalf("stored subscriptions = %v, want [%s]", got, sub.Endpoint)
			}
			if devices := f.deviceTokens(); len(devices) != 1 || devices[0] != deviceToken(0) {
				t.Fatalf("stored device tokens = %v, want [%s]", devices, deviceToken(0))
			}
		})
	}
}

func TestReconcileGrantsFirstBootAdoptsExistingSet(t *testing.T) {
	f := newPushFixture(t)
	sub := testSubscription(t, "https://push.example/reg/legacy")
	f.subscribe(sub)

	// No recorded surface yet (pre-upgrade rows): adopt, never purge.
	if err := f.sender.ReconcileGrants(context.Background(), "tok-a", "0.0.0.0"); err != nil {
		t.Fatalf("ReconcileGrants: %v", err)
	}
	if got := f.endpoints(); len(got) != 1 || got[0] != sub.Endpoint {
		t.Fatalf("stored subscriptions = %v, want [%s]", got, sub.Endpoint)
	}
}

func TestVAPIDKeyEndpointExposesOnlyThePublicKey(t *testing.T) {
	f := newPushFixture(t)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/push/vapid-key", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("vapid-key = %d, want 200", rec.Code)
	}
	want := fmt.Sprintf(`{"key":%q}`, f.sender.keys.Public)
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("vapid-key body = %s, want %s", got, want)
	}
	if strings.Contains(rec.Body.String(), f.sender.keys.Private) {
		t.Fatal("vapid-key response must never carry the private key")
	}
}

func TestFanoutDeliversEncryptedToEverySubscription(t *testing.T) {
	f := newPushFixture(t)
	subs := []webpush.Subscription{
		testSubscription(t, "https://push.example/reg/a"),
		testSubscription(t, "https://push.example/reg/b"),
	}
	for _, sub := range subs {
		f.client.status[sub.Endpoint] = http.StatusCreated
		f.subscribe(sub)
	}

	payload := PushPayload{Type: "interaction.question", Subject: "s1", Title: "Deploy?", Body: "pick one", Urgency: PushUrgencyHigh}
	if err := f.sender.Fanout(context.Background(), payload); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	reqs := f.client.recorded()
	if len(reqs) != 2 {
		t.Fatalf("fanout sent %d requests, want 2", len(reqs))
	}
	hit := map[string]bool{}
	for _, r := range reqs {
		hit[r.url] = true
		if got := r.header.Get("Authorization"); !strings.HasPrefix(got, "vapid ") {
			t.Fatalf("Authorization = %q, want a vapid signature (never a bearer token)", got)
		}
		if got := r.header.Get("TTL"); got != "3600" {
			t.Fatalf("TTL = %q, want 3600", got)
		}
		if got := r.header.Get("Urgency"); got != "high" {
			t.Fatalf("Urgency = %q, want high", got)
		}
		if got := r.header.Get("Content-Encoding"); got != "aes128gcm" {
			t.Fatalf("Content-Encoding = %q, want aes128gcm", got)
		}
		if bytes.Contains(r.body, []byte(`"type"`)) {
			t.Fatal("request body carries the plaintext payload; it must be encrypted")
		}
	}
	for _, sub := range subs {
		if !hit[sub.Endpoint] {
			t.Fatalf("no push delivered to %s (hit %v)", sub.Endpoint, hit)
		}
	}
}

func TestFanoutPrunesGoneSubscriptions(t *testing.T) {
	for _, tc := range []struct {
		id   string
		gone int
	}{
		{id: "404 not found", gone: http.StatusNotFound},
		{id: "410 gone", gone: http.StatusGone},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newPushFixture(t)
			healthy := testSubscription(t, "https://push.example/reg/healthy")
			gone := testSubscription(t, "https://push.example/reg/gone")
			f.client.status[healthy.Endpoint] = http.StatusCreated
			f.client.status[gone.Endpoint] = tc.gone
			f.subscribe(healthy)
			f.subscribe(gone)

			if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
				t.Fatalf("Fanout: %v (a gone subscription is pruned, not a failure)", err)
			}

			if got := f.endpoints(); len(got) != 1 || got[0] != healthy.Endpoint {
				t.Fatalf("endpoints after prune = %v, want only %s", got, healthy.Endpoint)
			}
			events := f.events()
			last := events[len(events)-1]
			if last.Type != EventPushUnsubscribe || last.Endpoint != gone.Endpoint {
				t.Fatalf("last event = %+v, want %s for %s", last, EventPushUnsubscribe, gone.Endpoint)
			}

			// The next fan-out only reaches the surviving endpoint.
			before := len(f.client.recorded())
			if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "again"}); err != nil {
				t.Fatalf("second Fanout: %v", err)
			}
			after := f.client.recorded()[before:]
			if len(after) != 1 || after[0].url != healthy.Endpoint {
				t.Fatalf("second fanout hit %+v, want only %s", after, healthy.Endpoint)
			}
		})
	}
}

func TestFanoutSurfacesNonGoneFailuresWithoutPruning(t *testing.T) {
	f := newPushFixture(t)
	sub := testSubscription(t, "https://push.example/reg/flaky")
	f.client.status[sub.Endpoint] = http.StatusTooManyRequests
	f.subscribe(sub)

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), sub.Endpoint) || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Fanout error = %v, want a failure naming %s and status 429", err, sub.Endpoint)
	}
	if got := f.endpoints(); len(got) != 1 {
		t.Fatalf("endpoints after transient failure = %v, want the subscription kept", got)
	}
}

func TestPushPayloadShape(t *testing.T) {
	for _, tc := range []struct {
		id      string
		payload PushPayload
		want    string
	}{
		{
			id:      "question with every field",
			payload: PushPayload{Type: "interaction.question", Subject: "s1", Title: "Deploy?", Body: "pick one", Urgency: "high"},
			want:    `{"type":"interaction.question","subject":"s1","title":"Deploy?","body":"pick one","urgency":"high"}`,
		},
		{
			id:      "notification omits empty title and urgency",
			payload: PushPayload{Type: "interaction.notification", Subject: "s1", Body: "done"},
			want:    `{"type":"interaction.notification","subject":"s1","body":"done"}`,
		},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("payload = %s, want %s", got, tc.want)
			}
		})
	}
}
