package interaction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/paths"
)

// shortTempHome returns a short-pathed temp HOME so the daemon's unix socket
// path stays under the macOS sun_path limit (~104 bytes), which t.TempDir()'s
// long /var/folders path would blow past.
func shortTempHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccr")
	if err != nil {
		t.Fatalf("mkdir temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Clean(dir)
}

const (
	testSession = "sess-1"
	testPID     = 4242
	testScope   = "/scope/a"
)

// harness is a real daemon driven over its unix socket: handlers run against an
// ephemeral on-disk SQLite the daemon owns, exactly as in production.
type harness struct {
	t      *testing.T
	client *daemon.Client
	paths  paths.Paths
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("HOME", shortTempHome(t))
	p := paths.Paths{App: ".ccr"}

	s, err := daemon.New(daemon.Config{
		AppName:         "cc-runtime-test",
		Paths:           p,
		Version:         "v0.0.0-test",
		ActiveStatuses:  ActiveStatuses,
		WindowAlive:     func(int) bool { return true },
		Gate:            Gate(),
		GateErrorReason: GateErrorReason,
		Migrate:         Migrate,
	})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	Register(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop within 5s")
		}
	})

	client := daemon.NewClient(p.SocketPath())
	deadline := time.Now().Add(5 * time.Second)
	for !client.Available() {
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &harness{t: t, client: client, paths: p}
}

func (h *harness) do(env daemon.Envelope) daemon.Reply {
	h.t.Helper()
	r, err := h.client.Do(context.Background(), env)
	if err != nil {
		h.t.Fatalf("Do(%s): %v", env.Op, err)
	}
	return r
}

func (h *harness) agentEnv(op daemon.Op, body any) daemon.Envelope {
	h.t.Helper()
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	return daemon.Envelope{Op: op, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: raw}
}

func (h *harness) start() string {
	h.t.Helper()
	r := h.do(h.agentEnv(OpStart, nil))
	if !r.OK {
		h.t.Fatalf("start: %s", r.Error)
	}
	return r.SubjectID
}

func (h *harness) ask(q QuestionPayload) (string, int64) {
	h.t.Helper()
	r := h.do(h.agentEnv(OpAsk, q))
	if !r.OK {
		h.t.Fatalf("ask: %s", r.Error)
	}
	var reply askReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		h.t.Fatalf("unmarshal ask reply: %v", err)
	}
	return reply.SubjectID, reply.QuestionID
}

func (h *harness) answer(subjectID string, questionID int64) bool {
	h.t.Helper()
	body, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"yes"}})
	r := h.do(daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: body})
	if !r.OK {
		h.t.Fatalf("answer: %s", r.Error)
	}
	var reply answerReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		h.t.Fatalf("unmarshal answer reply: %v", err)
	}
	return reply.Idled
}

func (h *harness) status(subjectID string) string {
	h.t.Helper()
	var got string
	if err := h.queryRow(subjectID, &got); err != nil {
		h.t.Fatalf("read status: %v", err)
	}
	return got
}

// queryRow reads a subject's status through the list op, the only read surface
// the package exposes over the socket.
func (h *harness) queryRow(subjectID string, dst *string) error {
	r := h.do(daemon.Envelope{Op: OpList, Scope: testScope})
	var reply listReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		return err
	}
	for _, s := range reply.Subjects {
		if s.SubjectID == subjectID {
			*dst = s.Status
			return nil
		}
	}
	*dst = StatusClosed
	return nil
}

func (h *harness) gateAllows() bool {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"tool_name": "Edit", "tool_input": json.RawMessage(`{"file_path":"x.go"}`)})
	r := h.do(daemon.Envelope{Op: daemon.OpGuardEdit, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: body})
	if !r.OK {
		h.t.Fatalf("guard-edit: %s", r.Error)
	}
	return r.Allow
}

func sampleQuestion(header string) QuestionPayload {
	return QuestionPayload{
		Header:  header,
		Prompt:  "pick one",
		Options: []Option{{Label: "yes"}, {Label: "no"}},
	}
}

func TestHandleAskAppendsAwaitsAndProjects(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("deploy?"))

	if subjectID == "" || questionID != 1 {
		t.Fatalf("ask = (%q, %d), want a subject id and question_id 1 (first event seq)", subjectID, questionID)
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after ask = %q, want awaiting", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must block edits while awaiting")
	}

	pendBody, _ := json.Marshal(subjectBody{SubjectID: subjectID})
	r := h.do(daemon.Envelope{Op: OpPending, Scope: testScope, Body: pendBody})
	var pend pendingReply
	if err := json.Unmarshal(r.Body, &pend); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if len(pend.Questions) != 1 || pend.Questions[0].QuestionID != questionID || pend.Questions[0].Header != "deploy?" {
		t.Fatalf("pending = %+v, want one question id %d header deploy?", pend.Questions, questionID)
	}
}

func TestHandleNotifyDoesNotChangeStatus(t *testing.T) {
	h := newHarness(t)
	subjectID := h.start()
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after start = %q, want idle", got)
	}

	r := h.do(h.agentEnv(OpNotify, NotificationPayload{Message: "heads up"}))
	if !r.OK {
		t.Fatalf("notify: %s", r.Error)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after notify = %q, want unchanged idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits when idle")
	}
}

func TestHandleAnswerOnlyQuestionIdles(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("ship?"))

	if idled := h.answer(subjectID, questionID); !idled {
		t.Fatal("answering the only open question must idle the subject")
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after answer = %q, want idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits after the subject idles")
	}
}

func TestMultiAskStaysAwaitingUntilAllAnswered(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	_, q2 := h.ask(sampleQuestion("q2"))

	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status with two open = %q, want awaiting", got)
	}

	if idled := h.answer(subjectID, q1); idled {
		t.Fatal("answering q1 must not idle while q2 is still open")
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after answering q1 = %q, want still awaiting", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must still block while q2 is open")
	}

	if idled := h.answer(subjectID, q2); !idled {
		t.Fatal("answering the last open question must idle the subject")
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after answering q2 = %q, want idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits once all questions are answered")
	}
}

func TestConcurrentAnswerRaceIdlesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	_, q2 := h.ask(sampleQuestion("q2"))

	var wg sync.WaitGroup
	var mu sync.Mutex
	idledCount := 0
	answers := []int64{q1, q2}
	for _, qid := range answers {
		wg.Add(1)
		go func(qid int64) {
			defer wg.Done()
			body, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: qid, Selected: []string{"yes"}})
			r, err := h.client.Do(context.Background(), daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: body})
			if err != nil {
				t.Errorf("concurrent answer: %v", err)
				return
			}
			var reply answerReply
			if err := json.Unmarshal(r.Body, &reply); err != nil {
				t.Errorf("unmarshal concurrent answer: %v", err)
				return
			}
			if reply.Idled {
				mu.Lock()
				idledCount++
				mu.Unlock()
			}
		}(qid)
	}
	wg.Wait()

	if idledCount != 1 {
		t.Fatalf("idled reported %d times, want exactly once", idledCount)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("final status = %q, want idle", got)
	}
}

func TestHandleAnswerPollReportsAnswer(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("color?"))

	pollBodyJSON, _ := json.Marshal(pollBody{SubjectID: subjectID, QuestionID: questionID})
	r := h.do(daemon.Envelope{Op: OpAnswerPoll, Scope: testScope, Body: pollBodyJSON})
	var before pollReply
	if err := json.Unmarshal(r.Body, &before); err != nil {
		t.Fatalf("unmarshal poll before: %v", err)
	}
	if before.Answered {
		t.Fatalf("poll before answer = %+v, want answered false", before)
	}

	h.answer(subjectID, questionID)

	r = h.do(daemon.Envelope{Op: OpAnswerPoll, Scope: testScope, Body: pollBodyJSON})
	var after pollReply
	if err := json.Unmarshal(r.Body, &after); err != nil {
		t.Fatalf("unmarshal poll after: %v", err)
	}
	if !after.Answered {
		t.Fatalf("poll after answer = %+v, want answered true", after)
	}
	var ans AnswerPayload
	if err := json.Unmarshal(after.Answer, &ans); err != nil {
		t.Fatalf("unmarshal poll answer payload: %v", err)
	}
	if ans.QuestionID != questionID || len(ans.Selected) != 1 || ans.Selected[0] != "yes" {
		t.Fatalf("poll answer = %+v, want question %d selected [yes]", ans, questionID)
	}
}

func TestHandleListReturnsAwaitingSubjectWithPendingCount(t *testing.T) {
	h := newHarness(t)
	subjectID, _ := h.ask(sampleQuestion("q1"))
	h.ask(sampleQuestion("q2"))

	r := h.do(daemon.Envelope{Op: OpList, Scope: testScope})
	var reply listReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(reply.Subjects) != 1 {
		t.Fatalf("list = %+v, want one subject", reply.Subjects)
	}
	got := reply.Subjects[0]
	if got.SubjectID != subjectID || got.Status != StatusAwaiting || got.Pending != 2 {
		t.Fatalf("listed subject = %+v, want %s awaiting pending 2", got, subjectID)
	}
}
