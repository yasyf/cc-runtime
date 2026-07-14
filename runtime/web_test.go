package runtime

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-runtime/interaction"
)

// httpBase returns the production daemon's HTTP origin from the published
// handshake.
func (e *e2e) httpBase() string {
	e.t.Helper()
	b, err := os.ReadFile(interaction.AppPaths().HTTPInfoPath())
	if err != nil {
		e.t.Fatalf("read http handshake: %v", err)
	}
	var info struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		e.t.Fatalf("unmarshal http handshake %q: %v", b, err)
	}
	if info.Port == 0 {
		e.t.Fatalf("http handshake %q carries no port", b)
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(info.Port))
}

func (e *e2e) httpGet(url string) (int, string, string) {
	e.t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		e.t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read GET %s body: %v", url, err)
	}
	return resp.StatusCode, string(body), resp.Header.Get("Content-Type")
}

// TestHTTPPlaneServesSPAAndInteractionREST drives the production buildServer
// composition over HTTP: the embedded SPA shell at / (with the deep-link
// fallback), and the interaction REST plane end to end through gate release.
func TestHTTPPlaneServesSPAAndInteractionREST(t *testing.T) {
	e := newE2E(t)
	base := e.httpBase()

	status, index, ct := e.httpGet(base + "/")
	if status != http.StatusOK || !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / = %d %s, want 200 text/html", status, ct)
	}
	if !strings.Contains(index, "<title>cc-runtime</title>") {
		t.Fatalf("GET / body %q, want the embedded SPA shell", index)
	}
	if status, deep, _ := e.httpGet(base + "/subjects/some-deep-link"); status != http.StatusOK || deep != index {
		t.Fatalf("deep link = %d, want 200 with the same SPA shell", status)
	}

	subjectID, questionID := e.ask(e2eQuestion("via-http?"))
	var sessions struct {
		Subjects []interaction.ListedSubject `json:"subjects"`
	}
	status, body, _ := e.httpGet(base + "/api/sessions")
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d %s, want 200", status, body)
	}
	if err := json.Unmarshal([]byte(body), &sessions); err != nil {
		t.Fatalf("unmarshal sessions %q: %v", body, err)
	}
	if len(sessions.Subjects) != 1 {
		t.Fatalf("sessions = %+v, want the one awaiting subject", sessions.Subjects)
	}
	got := sessions.Subjects[0]
	if got.SubjectID != subjectID || got.Scope != e2eScope || got.Status != interaction.StatusAwaiting || got.Pending != 1 {
		t.Fatalf("session = %+v, want %s %s awaiting pending 1", got, subjectID, e2eScope)
	}

	answer, _ := json.Marshal(interaction.AnswerPayload{QuestionID: questionID, Selected: []string{"yes"}})
	resp, err := http.Post(base+"/api/subjects/"+subjectID+"/answer", "application/json", strings.NewReader(string(answer)))
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	defer resp.Body.Close()
	answered, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read answer body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(answered)) != `{"idled":true}` {
		t.Fatalf("POST answer = %d %s, want 200 {\"idled\":true}", resp.StatusCode, answered)
	}
	if allow, _ := e.gateGuardEdit(); !allow {
		t.Fatal("gate must allow edits after the HTTP answer idles the subject")
	}
}
