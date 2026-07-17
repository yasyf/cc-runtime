package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"
)

const restToken = "rest-s3cret-token"

// spoofAddrListener wraps accepted connections so they report a fixed
// non-loopback peer over a real socket, mirroring cc-interact's auth tests, so
// the daemon's loopback bypass does not apply and token auth is exercised.
type spoofAddrListener struct {
	net.Listener
	addr net.Addr
}

func (l spoofAddrListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return spoofAddrConn{Conn: conn, addr: l.addr}, nil
}

type spoofAddrConn struct {
	net.Conn
	addr net.Addr
}

func (c spoofAddrConn) RemoteAddr() net.Addr { return c.addr }

// restHarness is the socket harness plus the daemon's real HTTP plane: base is
// the primary loopback listener, remote a listener whose peers appear
// non-loopback, so both sides of the auth boundary are reachable.
type restHarness struct {
	*harness
	base   string
	remote string
}

func newRESTHarness(t *testing.T) *restHarness {
	t.Helper()
	var remoteAddr string
	h := newHarness(t, func(cfg *daemon.Config) {
		cfg.HTTPToken = restToken
		cfg.ExtraHTTPListeners = []func(context.Context) (net.Listener, error){
			func(context.Context) (net.Listener, error) {
				inner, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return nil, err
				}
				remoteAddr = inner.Addr().String()
				return spoofAddrListener{
					Listener: inner,
					addr:     &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 41000},
				}, nil
			},
		}
	})

	b, err := os.ReadFile(h.paths.HTTPInfoPath())
	if err != nil {
		t.Fatalf("read http handshake: %v", err)
	}
	var info struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatalf("unmarshal http handshake %q: %v", b, err)
	}
	if info.Port == 0 {
		t.Fatalf("http handshake %q carries no port", b)
	}
	return &restHarness{
		harness: h,
		base:    "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(info.Port)),
		remote:  "http://" + remoteAddr,
	}
}

// request performs one HTTP call and returns the status and body.
func (h *restHarness) request(method, url, body, bearer string) (int, string) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err == nil && method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		h.t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read %s %s body: %v", method, url, err)
	}
	return resp.StatusCode, string(got)
}

func (h *restHarness) getJSON(path string, into any) {
	h.t.Helper()
	status, body := h.request(http.MethodGet, h.base+path, "", "")
	if status != http.StatusOK {
		h.t.Fatalf("GET %s = %d %s, want 200", path, status, body)
	}
	if err := json.Unmarshal([]byte(body), into); err != nil {
		h.t.Fatalf("unmarshal GET %s body %q: %v", path, body, err)
	}
}

// restAnswer POSTs an answer over the loopback listener and returns the status
// and body.
func (h *restHarness) restAnswer(subjectID string, a AnswerPayload) (int, string) {
	h.t.Helper()
	body, err := json.Marshal(a)
	if err != nil {
		h.t.Fatalf("marshal answer: %v", err)
	}
	return h.request(http.MethodPost, h.base+"/api/subjects/"+subjectID+"/answer", string(body), "")
}

func TestRESTSessionsListsActiveSubjectsAcrossScopes(t *testing.T) {
	h := newRESTHarness(t)
	subjectA, _ := h.ask(sampleQuestion("q1"))
	h.ask(sampleQuestion("q2"))
	r := h.do(daemon.Envelope{Op: OpStart, Session: "sess-2", ClaudePID: 777, Scope: "/scope/b"})
	if !r.OK {
		t.Fatalf("start scope b: %s", r.Error)
	}
	subjectB := r.SubjectID

	var got sessionsReply
	h.getJSON("/api/sessions", &got)
	want := []ListedSubject{
		{SubjectID: subjectB, Scope: "/scope/b", Status: StatusIdle, Pending: 0},
		{SubjectID: subjectA, Scope: testScope, Status: StatusAwaiting, Pending: 2},
	}
	if len(got.Subjects) != len(want) {
		t.Fatalf("sessions = %+v, want %+v", got.Subjects, want)
	}
	for i := range want {
		if got.Subjects[i] != want[i] {
			t.Fatalf("sessions[%d] = %+v, want %+v", i, got.Subjects[i], want[i])
		}
	}
}

func TestRESTPendingReturnsFullPayloads(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("deploy?"))
	_, q2 := h.ask(sampleQuestion("ship?"))

	var got pendingReply
	h.getJSON("/api/subjects/"+subjectID+"/pending", &got)
	if len(got.Questions) != 2 || got.Questions[0].QuestionID != q1 || got.Questions[1].QuestionID != q2 {
		t.Fatalf("pending = %+v, want questions %d then %d", got.Questions, q1, q2)
	}
	if got.Questions[0].Header != "deploy?" || got.Questions[1].Header != "ship?" {
		t.Fatalf("pending headers = %+v, want deploy? then ship?", got.Questions)
	}
	var payload QuestionPayload
	if err := json.Unmarshal([]byte(got.Questions[0].Payload), &payload); err != nil {
		t.Fatalf("unmarshal question payload %q: %v", got.Questions[0].Payload, err)
	}
	if payload.Prompt != "pick one" || len(payload.Options) != 2 || payload.Options[0].Label != "yes" || payload.Options[1].Label != "no" {
		t.Fatalf("question payload = %+v, want the full sampleQuestion context", payload)
	}
}

func TestRESTPending404sUnknownSubject(t *testing.T) {
	h := newRESTHarness(t)
	status, body := h.request(http.MethodGet, h.base+"/api/subjects/no-such-subject/pending", "", "")
	if status != http.StatusNotFound || !strings.Contains(body, "unknown subject") {
		t.Fatalf("pending unknown subject = %d %q, want 404 naming the unknown subject", status, body)
	}
}

func TestRESTAnswerReleasesGate(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("merge?"))
	if h.gateAllows() {
		t.Fatal("gate must block edits while the question is open")
	}

	status, body := h.restAnswer(subjectID, AnswerPayload{QuestionID: questionID, Selected: []string{"yes"}})
	if status != http.StatusOK || strings.TrimSpace(body) != `{"idled":true}` {
		t.Fatalf("answer = %d %q, want 200 {\"idled\":true}", status, body)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after REST answer = %q, want idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits after the REST answer idles the subject")
	}
}

func TestRESTAnswerIsIdempotent(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("color?"))

	first := AnswerPayload{QuestionID: questionID, Selected: []string{"yes"}}
	if status, body := h.restAnswer(subjectID, first); status != http.StatusOK || strings.TrimSpace(body) != `{"idled":true}` {
		t.Fatalf("first answer = %d %q, want 200 {\"idled\":true}", status, body)
	}
	// A retried answer with a different pick is a no-op: 200, still idled, and
	// the recorded answer stays the first one.
	if status, body := h.restAnswer(subjectID, AnswerPayload{QuestionID: questionID, Selected: []string{"no"}}); status != http.StatusOK || strings.TrimSpace(body) != `{"idled":true}` {
		t.Fatalf("retried answer = %d %q, want 200 {\"idled\":true}", status, body)
	}

	pollBodyJSON, _ := json.Marshal(pollBody{SubjectID: subjectID, QuestionID: questionID})
	r := h.do(daemon.Envelope{Op: OpAnswerPoll, Scope: testScope, Body: pollBodyJSON})
	var reply pollReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		t.Fatalf("unmarshal poll: %v", err)
	}
	if !reply.Answered {
		t.Fatal("poll after answers must report answered")
	}
	var recorded AnswerPayload
	if err := json.Unmarshal(reply.Answer, &recorded); err != nil {
		t.Fatalf("unmarshal recorded answer: %v", err)
	}
	if len(recorded.Selected) != 1 || recorded.Selected[0] != "yes" {
		t.Fatalf("recorded answer = %+v, want the first answer [yes] kept (never overwritten)", recorded)
	}
}

func TestRESTAnswerFailsLoud(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("q1"))

	for _, tc := range []struct {
		id         string
		subject    string
		body       string
		wantStatus int
		wantIn     string
	}{
		{
			id: "malformed body", subject: subjectID, body: "{",
			wantStatus: http.StatusBadRequest, wantIn: "bad answer body",
		},
		{
			id: "unknown subject", subject: "no-such-subject", body: fmt.Sprintf(`{"question_id":%d,"selected":["yes"]}`, questionID),
			wantStatus: http.StatusNotFound, wantIn: "unknown question",
		},
		{
			id: "unknown question id", subject: subjectID, body: `{"question_id":999,"selected":["yes"]}`,
			wantStatus: http.StatusNotFound, wantIn: "unknown question",
		},
		{
			id: "missing question id", subject: subjectID, body: `{"selected":["yes"]}`,
			wantStatus: http.StatusNotFound, wantIn: "unknown question",
		},
		{
			id: "body subject_id disagrees with path", subject: subjectID, body: fmt.Sprintf(`{"subject_id":"other","question_id":%d}`, questionID),
			wantStatus: http.StatusBadRequest, wantIn: "disagrees with path subject",
		},
	} {
		t.Run(tc.id, func(t *testing.T) {
			status, body := h.request(http.MethodPost, h.base+"/api/subjects/"+tc.subject+"/answer", tc.body, "")
			if status != tc.wantStatus || !strings.Contains(body, tc.wantIn) {
				t.Fatalf("answer = %d %q, want %d containing %q", status, body, tc.wantStatus, tc.wantIn)
			}
		})
	}

	// No failed answer released the gate: the question is still open.
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after failed answers = %q, want still awaiting", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must still block after failed answers")
	}
}

// TestRESTAnswerRejectsNonJSONContentType pins the CSRF guard: a cross-site
// "simple" POST (no CORS preflight needed) must never answer a question, even
// when the loopback or trusted-peer bypass admits the request.
func TestRESTAnswerRejectsNonJSONContentType(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("merge?"))
	body := fmt.Sprintf(`{"question_id":%d,"selected":["yes"]}`, questionID)

	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		t.Run("ct="+ct, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, h.base+"/api/subjects/"+subjectID+"/answer", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("answer with Content-Type %q = %d, want 415", ct, resp.StatusCode)
			}
		})
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after non-JSON answers = %q, want still awaiting (gate held)", got)
	}
}

func TestRESTRoutesRequireTokenForNonLoopbackPeers(t *testing.T) {
	h := newRESTHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("q1"))
	answerBody := fmt.Sprintf(`{"question_id":%d,"selected":["yes"]}`, questionID)

	for _, tc := range []struct {
		id            string
		method        string
		path          string
		body          string
		wantWithToken int
	}{
		{id: "sessions", method: http.MethodGet, path: "/api/sessions", wantWithToken: http.StatusOK},
		{id: "pending", method: http.MethodGet, path: "/api/subjects/" + subjectID + "/pending", wantWithToken: http.StatusOK},
		{id: "answer", method: http.MethodPost, path: "/api/subjects/" + subjectID + "/answer", body: answerBody, wantWithToken: http.StatusOK},
	} {
		t.Run(tc.id, func(t *testing.T) {
			status, body := h.request(tc.method, h.remote+tc.path, tc.body, "")
			if status != http.StatusUnauthorized || body != "unauthorized\n" {
				t.Fatalf("%s %s without token = %d %q, want 401 unauthorized", tc.method, tc.path, status, body)
			}
			status, body = h.request(tc.method, h.remote+tc.path, tc.body, "wrong-token")
			if status != http.StatusUnauthorized || body != "unauthorized\n" {
				t.Fatalf("%s %s with wrong token = %d %q, want 401 unauthorized", tc.method, tc.path, status, body)
			}
			if status, body = h.request(tc.method, h.remote+tc.path, tc.body, restToken); status != tc.wantWithToken {
				t.Fatalf("%s %s with token = %d %q, want %d", tc.method, tc.path, status, body, tc.wantWithToken)
			}
		})
	}

	// The tokened remote answer above released the gate, proving the remote
	// plane is fully functional, not just reachable.
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after remote tokened answer = %q, want idle", got)
	}
}
