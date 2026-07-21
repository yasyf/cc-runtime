package access

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/yasyf/cc-interact/event"
	"github.com/yasyf/cc-interact/store"
)

type recordedAPNSPush struct {
	url    string
	header http.Header
	body   []byte
}

// fakeAPNSClient is the mocked network boundary: it records every outbound
// APNs request and answers with the status + reason (and, when configured,
// the 410 invalidation timestamp) configured per token.
type fakeAPNSClient struct {
	mu        sync.Mutex
	status    map[string]int
	reason    map[string]string
	timestamp map[string]int64
	requests  []recordedAPNSPush
}

func (f *fakeAPNSClient) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	token := path.Base(req.URL.Path)
	code, ok := f.status[token]
	if !ok {
		return nil, fmt.Errorf("unexpected apns push to %s", req.URL)
	}
	f.requests = append(f.requests, recordedAPNSPush{url: req.URL.String(), header: req.Header.Clone(), body: body})
	var respBody []byte
	if reason := f.reason[token]; reason != "" {
		if ts := f.timestamp[token]; ts != 0 {
			respBody = fmt.Appendf(nil, `{"reason":%q,"timestamp":%d}`, reason, ts)
		} else {
			respBody = fmt.Appendf(nil, `{"reason":%q}`, reason)
		}
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(respBody))}, nil
}

func (f *fakeAPNSClient) recorded() []recordedAPNSPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedAPNSPush(nil), f.requests...)
}

type apnsFixture struct {
	t      *testing.T
	store  *store.Store
	sender *APNSSender
	client *fakeAPNSClient
	mux    *http.ServeMux
	pubKey any
	now    time.Time
}

func newAPNSFixture(t *testing.T) *apnsFixture {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), func(ctx context.Context, db *sql.DB) error {
		if err := PushMigrate(ctx, db); err != nil {
			return err
		}
		return APNSMigrate(ctx, db)
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	keyPath, key := writeAPNSKey(t)
	loaded, err := LoadAPNSKey(keyPath)
	if err != nil {
		t.Fatalf("LoadAPNSKey: %v", err)
	}
	client := &fakeAPNSClient{status: map[string]int{}, reason: map[string]string{}, timestamp: map[string]int64{}}
	f := &apnsFixture{t: t, store: st, client: client, pubKey: &key.PublicKey, now: time.Unix(1_752_000_000, 0)}
	f.sender = &APNSSender{
		auth:   &apnsAuth{key: loaded, keyID: "KEY123", teamID: "TEAM99"},
		topic:  "com.example.cc",
		host:   apnsHost,
		db:     st.DB(),
		append: st.AppendEvent,
		client: client,
		now:    func() time.Time { return f.now },
	}
	f.mux = http.NewServeMux()
	MountAPNS(f.mux, f.sender, apnsTestBearer)
	return f
}

// apnsTestBearer is the pair bearer the fixture mounts registration under.
const apnsTestBearer = "pair-bearer-1"

func (f *apnsFixture) post(body string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/push/device-tokens", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apnsTestBearer)
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *apnsFixture) register(token string) {
	f.t.Helper()
	rec := f.post(fmt.Sprintf(`{"token":%q,"platform":"ios"}`, token))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("register %s = %d %s, want 200", token, rec.Code, rec.Body)
	}
}

func (f *apnsFixture) tokens() []string {
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

func (f *apnsFixture) events() []apnsEventFrame {
	f.t.Helper()
	evs, err := f.store.EventsSince(context.Background(), PushSubjectID, 0, "")
	if err != nil {
		f.t.Fatalf("EventsSince: %v", err)
	}
	out := make([]apnsEventFrame, len(evs))
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

// deviceToken mints a distinct 64-char lowercase-hex token, the shape Apple
// issues today.
func deviceToken(i int) string {
	return fmt.Sprintf("%064x", i+1)
}

func TestRegisterDeviceTokenRoundTripDedupes(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)

	rec := f.post(fmt.Sprintf(`{"token":%q,"platform":"ios"}`, token))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("register = %d %q, want 200 {\"ok\":true}", rec.Code, rec.Body)
	}

	if got := f.tokens(); len(got) != 1 || got[0] != token {
		t.Fatalf("projected tokens = %v, want [%s]", got, token)
	}
	events := f.events()
	if len(events) != 1 || events[0].Type != EventPushDeviceRegister || events[0].Token != token || events[0].Platform != "ios" {
		t.Fatalf("events = %+v, want one %s for %s (ios)", events, EventPushDeviceRegister, token)
	}

	// A repeated registration is an idempotent no-op: one row, one event.
	f.register(token)
	if got := f.tokens(); len(got) != 1 {
		t.Fatalf("tokens after re-register = %v, want still one", got)
	}
	if events := f.events(); len(events) != 1 {
		t.Fatalf("events after re-register = %+v, want still one (deduped by token)", events)
	}
}

// TestRegisterDeviceTokenRequiresThePairBearer pins the loopback-bypass fix:
// the daemon's auth layer admits tokenless loopback peers, so the handler must
// demand the bearer itself before minting a durable delivery grant.
func TestRegisterDeviceTokenRequiresThePairBearer(t *testing.T) {
	body := fmt.Sprintf(`{"token":%q,"platform":"ios"}`, deviceToken(0))
	for _, tc := range []struct {
		id     string
		mount  string
		header string
	}{
		{id: "missing bearer", mount: apnsTestBearer},
		{id: "wrong bearer", mount: apnsTestBearer, header: "Bearer nope"},
		{id: "no token minted refuses even an empty bearer", mount: "", header: "Bearer "},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newAPNSFixture(t)
			mux := http.NewServeMux()
			MountAPNS(mux, f.sender, tc.mount)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/push/device-tokens", strings.NewReader(body))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("register = %d %s, want 401", rec.Code, rec.Body)
			}
			if got := f.tokens(); len(got) != 0 {
				t.Fatalf("projected tokens = %v, want none", got)
			}
			if events := f.events(); len(events) != 0 {
				t.Fatalf("events = %+v, want none", events)
			}
		})
	}
}

func TestRegisterDeviceTokenRejectsInvalidRegistrations(t *testing.T) {
	valid := deviceToken(0)
	for _, tc := range []struct {
		id       string
		body     string
		wantCode int
	}{
		{id: "malformed json", body: "{", wantCode: http.StatusBadRequest},
		{id: "missing platform", body: fmt.Sprintf(`{"token":%q}`, valid), wantCode: http.StatusBadRequest},
		{id: "wrong platform", body: fmt.Sprintf(`{"token":%q,"platform":"android"}`, valid), wantCode: http.StatusBadRequest},
		{id: "uppercase hex", body: fmt.Sprintf(`{"token":%q,"platform":"ios"}`, strings.Repeat("AB", 32)), wantCode: http.StatusBadRequest},
		{id: "non-hex characters", body: fmt.Sprintf(`{"token":%q,"platform":"ios"}`, strings.Repeat("zz", 32)), wantCode: http.StatusBadRequest},
		{id: "too short", body: fmt.Sprintf(`{"token":%q,"platform":"ios"}`, valid[:62]), wantCode: http.StatusBadRequest},
		{id: "odd length", body: fmt.Sprintf(`{"token":%q,"platform":"ios"}`, valid+"a"), wantCode: http.StatusBadRequest},
		{id: "too long", body: fmt.Sprintf(`{"token":%q,"platform":"ios"}`, strings.Repeat("ab", 101)), wantCode: http.StatusBadRequest},
		{id: "oversized body", body: `{"pad":"` + strings.Repeat("x", maxDeviceRegistrationBytes) + `"}`, wantCode: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newAPNSFixture(t)
			if rec := f.post(tc.body); rec.Code != tc.wantCode {
				t.Fatalf("register = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := f.tokens(); len(got) != 0 {
				t.Fatalf("projected tokens = %v, want none", got)
			}
			if events := f.events(); len(events) != 0 {
				t.Fatalf("events = %+v, want none", events)
			}
		})
	}
}

func TestRegisterDeviceTokenCapsStoredSet(t *testing.T) {
	f := newAPNSFixture(t)
	for i := range maxDeviceTokens {
		f.register(deviceToken(i))
	}

	over := deviceToken(maxDeviceTokens)
	if rec := f.post(fmt.Sprintf(`{"token":%q,"platform":"ios"}`, over)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("register over cap = %d %s, want 429", rec.Code, rec.Body)
	}
	if got := f.tokens(); len(got) != maxDeviceTokens {
		t.Fatalf("stored tokens = %d, want %d", len(got), maxDeviceTokens)
	}

	// Re-registering a stored token still passes at the cap.
	f.register(deviceToken(0))
	if got := f.tokens(); len(got) != maxDeviceTokens {
		t.Fatalf("stored tokens after re-register = %d, want %d", len(got), maxDeviceTokens)
	}
}

// TestRegisterDeviceTokenConcurrentAtCapHoldsBound races registrations for the
// last slot: exactly one wins, the rest refuse, and the stored set never
// overshoots.
func TestRegisterDeviceTokenConcurrentAtCapHoldsBound(t *testing.T) {
	f := newAPNSFixture(t)
	for i := range maxDeviceTokens - 1 {
		f.register(deviceToken(i))
	}

	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.sender.Register(context.Background(), deviceToken(maxDeviceTokens+i), "ios")
		}()
	}
	wg.Wait()

	var admitted, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrDeviceTokenLimit):
			refused++
		default:
			t.Fatalf("Register: %v", err)
		}
	}
	if admitted != 1 || refused != racers-1 {
		t.Fatalf("admitted = %d, refused = %d, want 1 and %d", admitted, refused, racers-1)
	}
	if got := f.tokens(); len(got) != maxDeviceTokens {
		t.Fatalf("stored tokens = %d, want %d", len(got), maxDeviceTokens)
	}
}

func TestAPNSFanoutDeliversToEveryDevice(t *testing.T) {
	f := newAPNSFixture(t)
	tokens := []string{deviceToken(0), deviceToken(1)}
	for _, token := range tokens {
		f.client.status[token] = http.StatusOK
		f.register(token)
	}

	payload := PushPayload{Type: "interaction.question", Subject: "s1", Title: "Deploy?", Body: "pick one", Urgency: PushUrgencyHigh}
	if err := f.sender.Fanout(context.Background(), payload); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	reqs := f.client.recorded()
	if len(reqs) != 2 {
		t.Fatalf("fanout sent %d requests, want 2", len(reqs))
	}
	wantBody := `{"aps":{"alert":{"title":"Deploy?","body":"pick one"},"sound":"default","thread-id":"s1"},"payload":{"type":"interaction.question","subject":"s1","urgency":"high"}}`
	wantExpiration := strconv.FormatInt(f.now.Add(pushTTL*time.Second).Unix(), 10)
	hit := map[string]bool{}
	for _, r := range reqs {
		bearer, ok := strings.CutPrefix(r.header.Get("Authorization"), "bearer ")
		if !ok {
			t.Fatalf("Authorization = %q, want a bearer provider token", r.header.Get("Authorization"))
		}
		parsed, err := jwt.Parse(bearer, func(t *jwt.Token) (any, error) { return f.pubKey, nil }, jwt.WithValidMethods([]string{"ES256"}))
		if err != nil || !parsed.Valid {
			t.Fatalf("provider token did not verify: %v", err)
		}
		if kid := parsed.Header["kid"]; kid != "KEY123" {
			t.Fatalf("kid = %v, want KEY123", kid)
		}
		if got := r.header.Get("Apns-Topic"); got != "com.example.cc" {
			t.Fatalf("apns-topic = %q, want com.example.cc", got)
		}
		if got := r.header.Get("Apns-Push-Type"); got != "alert" {
			t.Fatalf("apns-push-type = %q, want alert", got)
		}
		if got := r.header.Get("Apns-Priority"); got != "10" {
			t.Fatalf("apns-priority = %q, want 10 for high urgency", got)
		}
		if got := r.header.Get("Apns-Expiration"); got != wantExpiration {
			t.Fatalf("apns-expiration = %q, want %s", got, wantExpiration)
		}
		if got := string(r.body); got != wantBody {
			t.Fatalf("body = %s, want %s", got, wantBody)
		}
		if bytes.Contains(r.body, []byte(bearer)) {
			t.Fatal("request body carries the provider token; only the header may")
		}
		hit[strings.TrimPrefix(r.url, apnsHost+"/3/device/")] = true
	}
	for _, token := range tokens {
		if !hit[token] {
			t.Fatalf("no push delivered to %s (hit %v)", token, hit)
		}
	}
}

func TestAPNSFanoutMapsUrgencyAndTitle(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusOK
	f.register(token)

	payload := PushPayload{Type: "interaction.notification", Subject: "s1", Body: "done", Urgency: "normal"}
	if err := f.sender.Fanout(context.Background(), payload); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	reqs := f.client.recorded()
	if len(reqs) != 1 {
		t.Fatalf("fanout sent %d requests, want 1", len(reqs))
	}
	if got := reqs[0].header.Get("Apns-Priority"); got != "5" {
		t.Fatalf("apns-priority = %q, want 5 for non-high urgency", got)
	}
	want := `{"aps":{"alert":{"body":"done"},"sound":"default","thread-id":"s1"},"payload":{"type":"interaction.notification","subject":"s1","urgency":"normal"}}`
	if got := string(reqs[0].body); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestAPNSFanoutReusesAndRefreshesProviderToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusOK
	f.register(token)

	fanout := func() string {
		t.Helper()
		before := len(f.client.recorded())
		if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
			t.Fatalf("Fanout: %v", err)
		}
		reqs := f.client.recorded()[before:]
		if len(reqs) != 1 {
			t.Fatalf("fanout sent %d requests, want 1", len(reqs))
		}
		return reqs[0].header.Get("Authorization")
	}

	first := fanout()
	f.now = f.now.Add(apnsTokenRefresh - time.Minute)
	if got := fanout(); got != first {
		t.Fatal("fanout inside the refresh window re-minted the provider token, want the cached one")
	}
	f.now = f.now.Add(2 * time.Minute)
	if got := fanout(); got == first {
		t.Fatal("fanout past the refresh window reused the provider token, want a fresh mint")
	}
}

func TestAPNSFanoutPrunesUnregisteredDevices(t *testing.T) {
	for _, tc := range []struct {
		id     string
		status int
		reason string
	}{
		{id: "410 unregistered", status: http.StatusGone, reason: "Unregistered"},
		{id: "400 bad device token", status: http.StatusBadRequest, reason: "BadDeviceToken"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newAPNSFixture(t)
			healthy, gone := deviceToken(0), deviceToken(1)
			f.client.status[healthy] = http.StatusOK
			f.client.status[gone] = tc.status
			f.client.reason[gone] = tc.reason
			f.register(healthy)
			f.register(gone)

			if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
				t.Fatalf("Fanout: %v (an unregistered device is pruned, not a failure)", err)
			}

			if got := f.tokens(); len(got) != 1 || got[0] != healthy {
				t.Fatalf("tokens after prune = %v, want only %s", got, healthy)
			}
			events := f.events()
			last := events[len(events)-1]
			if last.Type != EventPushDeviceUnregister || last.Token != gone {
				t.Fatalf("last event = %+v, want %s for %s", last, EventPushDeviceUnregister, gone)
			}

			// The next fan-out only reaches the surviving device.
			before := len(f.client.recorded())
			if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "again"}); err != nil {
				t.Fatalf("second Fanout: %v", err)
			}
			after := f.client.recorded()[before:]
			if len(after) != 1 || !strings.HasSuffix(after[0].url, "/3/device/"+healthy) {
				t.Fatalf("second fanout hit %+v, want only %s", after, healthy)
			}
		})
	}
}

// TestAPNSDelayed410KeepsAFreshlyReRegisteredToken covers the stale-response
// race: a re-registration refreshes created_at, so an in-flight 410 whose
// invalidation timestamp predates it must not delete the fresh grant.
func TestAPNSDelayed410KeepsAFreshlyReRegisteredToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusGone
	f.client.reason[token] = "Unregistered"
	f.register(token)
	invalidatedAt := f.now.UnixMilli()

	f.now = f.now.Add(time.Hour)
	f.register(token)
	f.client.timestamp[token] = invalidatedAt

	if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
		t.Fatalf("Fanout: %v (a stale 410 is dropped, not a failure)", err)
	}
	if got := f.tokens(); len(got) != 1 || got[0] != token {
		t.Fatalf("tokens after stale 410 = %v, want [%s] kept", got, token)
	}
	for _, ev := range f.events() {
		if ev.Type == EventPushDeviceUnregister {
			t.Fatalf("a stale 410 appended an unregister event: %+v", ev)
		}
	}

	// An invalidation at or after the registration still prunes.
	f.client.timestamp[token] = f.now.UnixMilli()
	if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if got := f.tokens(); len(got) != 0 {
		t.Fatalf("tokens after current 410 = %v, want pruned", got)
	}
}

func TestReRegisterRefreshesCreatedAt(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.register(token)

	f.now = f.now.Add(time.Hour)
	f.register(token)

	var createdAt int64
	if err := f.store.DB().QueryRow(`SELECT created_at FROM apns_device_tokens WHERE token=?`, token).Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if createdAt != f.now.UnixMilli() {
		t.Fatalf("created_at = %d, want refreshed to %d", createdAt, f.now.UnixMilli())
	}
}

func TestAPNSFanoutSurfacesOtherFailuresWithoutPruning(t *testing.T) {
	for _, tc := range []struct {
		id     string
		status int
		reason string
	}{
		{id: "403 invalid provider token", status: http.StatusForbidden, reason: "InvalidProviderToken"},
		{id: "400 non-token reason", status: http.StatusBadRequest, reason: "MissingTopic"},
		{id: "429 too many requests", status: http.StatusTooManyRequests, reason: "TooManyRequests"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newAPNSFixture(t)
			token := deviceToken(0)
			f.client.status[token] = tc.status
			f.client.reason[token] = tc.reason
			f.register(token)

			err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
			if err == nil || !strings.Contains(err.Error(), token[:8]) ||
				!strings.Contains(err.Error(), strconv.Itoa(tc.status)) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("Fanout error = %v, want a failure naming the token prefix %s, status %d, and reason %s", err, token[:8], tc.status, tc.reason)
			}
			// The full device token is a delivery credential and must never ride
			// the error string into logs.
			if strings.Contains(err.Error(), token) {
				t.Fatalf("Fanout error = %v, want the full device token redacted", err)
			}
			if got := f.tokens(); len(got) != 1 {
				t.Fatalf("tokens after failure = %v, want the registration kept", got)
			}
		})
	}
}

// erroringDoer fails every request the way http.Client does: a *url.Error whose
// message embeds the request URL — and so the device token in its path.
type erroringDoer struct{ err error }

func (d erroringDoer) Do(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: d.err}
}

func TestAPNSFanoutTransportErrorRedactsToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusOK
	f.register(token)
	f.sender.client = erroringDoer{err: errors.New("connection reset")}

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Fanout error = %v, want the token prefix %s and the transport cause", err, token[:8])
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Fanout error = %v, want the full device token (via the url.Error URL) redacted", err)
	}
}

// nestedURLErrorDoer fails every request with a url.Error nested inside another
// url.Error — both layers embedding the request URL, and so the device token.
type nestedURLErrorDoer struct{}

func (nestedURLErrorDoer) Do(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: req.Method, URL: req.URL.String(),
		Err: &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New("connection reset")}}
}

func TestAPNSFanoutNestedURLErrorRedactsToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusOK
	f.register(token)
	f.sender.client = nestedURLErrorDoer{}

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Fanout error = %v, want the token prefix %s and the transport cause", err, token[:8])
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Fanout error = %v, want the full device token (nested url.Error) redacted", err)
	}
}

// urlEchoingDoer fails with a bare error embedding the request URL without any
// url.Error wrapper — the shape a proxy or middleware transport can produce.
type urlEchoingDoer struct{}

func (urlEchoingDoer) Do(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("proxyconnect tcp: dial %s: connection refused", req.URL)
}

func TestAPNSFanoutRawURLCauseRedactsToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusOK
	f.register(token)
	f.sender.client = urlEchoingDoer{}

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Fanout error = %v, want the token prefix %s and the transport cause", err, token[:8])
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Fanout error = %v, want the full device token (raw URL cause) redacted", err)
	}
}

// TestAPNSPruneAppendErrorRedactsToken covers the prune path: the unregister
// event's payload carries the full token, so an AppendFunc error that echoes
// its event must not carry the token downstream.
func TestAPNSPruneAppendErrorRedactsToken(t *testing.T) {
	f := newAPNSFixture(t)
	token := deviceToken(0)
	f.client.status[token] = http.StatusGone
	f.client.reason[token] = "Unregistered"
	f.register(token)
	f.sender.append = func(_ context.Context, e *event.Event) (int64, error) {
		return 0, fmt.Errorf("append rejected: %s", e.Payload)
	}

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), "append device unregister") || !strings.Contains(err.Error(), token[:8]) {
		t.Fatalf("Fanout error = %v, want an append-unregister failure naming the token prefix %s", err, token[:8])
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Fanout error = %v, want the full device token (via the event payload) redacted", err)
	}
}

// TestAPNSShortTokenNeverRidesErrors pins redactToken's short-input contract: a
// token at or under the prefix length is elided, never echoed verbatim.
func TestAPNSShortTokenNeverRidesErrors(t *testing.T) {
	f := newAPNSFixture(t)
	const token = "ab12cd34"
	if err := f.sender.Register(context.Background(), token, "ios"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	f.sender.client = urlEchoingDoer{}

	err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Fanout error = %v, want the transport cause", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Fanout error = %v, want the short token elided, not echoed", err)
	}
}

func TestPurgeDeviceTokensRetiresEveryRegistration(t *testing.T) {
	f := newAPNSFixture(t)
	f.register(deviceToken(0))
	f.register(deviceToken(1))

	if err := PurgeDeviceTokens(context.Background(), f.store.DB(), f.store.AppendEvent); err != nil {
		t.Fatalf("PurgeDeviceTokens: %v", err)
	}

	if got := f.tokens(); len(got) != 0 {
		t.Fatalf("tokens after purge = %v, want none", got)
	}
	var unregistered []string
	for _, ev := range f.events() {
		if ev.Type == EventPushDeviceUnregister {
			unregistered = append(unregistered, ev.Token)
		}
	}
	if len(unregistered) != 2 || unregistered[0] != deviceToken(0) || unregistered[1] != deviceToken(1) {
		t.Fatalf("unregister events = %v, want [%s %s]", unregistered, deviceToken(0), deviceToken(1))
	}

	// An empty registry purges to a no-op: no further events.
	before := len(f.events())
	if err := PurgeDeviceTokens(context.Background(), f.store.DB(), f.store.AppendEvent); err != nil {
		t.Fatalf("PurgeDeviceTokens (empty): %v", err)
	}
	if got := len(f.events()); got != before {
		t.Fatalf("events after empty purge = %d, want %d", got, before)
	}
}

func TestAPNSFanoutWithoutDevicesIsANoOp(t *testing.T) {
	f := newAPNSFixture(t)
	if err := f.sender.Fanout(context.Background(), PushPayload{Type: "interaction.notification", Subject: "s1", Body: "hi"}); err != nil {
		t.Fatalf("Fanout with no devices: %v", err)
	}
	if got := f.client.recorded(); len(got) != 0 {
		t.Fatalf("fanout sent %d requests, want none", len(got))
	}
}

func TestNewAPNSSenderFailsLoudlyOnBrokenKey(t *testing.T) {
	f := newAPNSFixture(t)
	cfg := APNSConfig{KeyPath: filepath.Join(t.TempDir(), "missing.p8"), KeyID: "K", TeamID: "T", BundleID: "b"}
	if _, err := NewAPNSSender(cfg, f.store.DB(), f.store.AppendEvent); err == nil {
		t.Fatal("NewAPNSSender with a missing key must fail loudly, got nil error")
	}
}

func TestNewAPNSSenderPicksHostBySandbox(t *testing.T) {
	keyPath, _ := writeAPNSKey(t)
	f := newAPNSFixture(t)
	for _, tc := range []struct {
		id      string
		sandbox bool
		want    string
	}{
		{id: "production", sandbox: false, want: apnsHost},
		{id: "sandbox", sandbox: true, want: apnsSandboxHost},
	} {
		t.Run(tc.id, func(t *testing.T) {
			cfg := APNSConfig{KeyPath: keyPath, KeyID: "K", TeamID: "T", BundleID: "b", Sandbox: tc.sandbox}
			sender, err := NewAPNSSender(cfg, f.store.DB(), f.store.AppendEvent)
			if err != nil {
				t.Fatalf("NewAPNSSender: %v", err)
			}
			if sender.host != tc.want {
				t.Fatalf("host = %q, want %q", sender.host, tc.want)
			}
		})
	}
}
