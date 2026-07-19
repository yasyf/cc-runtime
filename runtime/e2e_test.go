package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/store"
	"github.com/yasyf/cc-interact/subject"

	"github.com/yasyf/cc-runtime/interaction"
)

const (
	e2eSession = "e2e-sess"
	e2ePID     = 51515
	e2eScope   = "/e2e/scope"
)

// e2e drives the real cc-runtime daemon — the production buildServer composition
// — over its unix socket against an ephemeral on-disk SQLite, exactly as in
// production. No nested claude: the test is the agent and the human at once.
type e2e struct {
	t        *testing.T
	client   *daemon.Client
	resolver subject.Resolver
}

// shortTempHome returns a short-pathed temp HOME so the daemon's unix socket
// stays under the macOS sun_path limit (~104 bytes); t.TempDir()'s long
// /var/folders path would blow past it.
func shortTempHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccre")
	if err != nil {
		t.Fatalf("mkdir temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Clean(dir)
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	t.Setenv("HOME", shortTempHome(t))

	s, err := buildServer(t.Context())
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

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

	deadline := time.Now().Add(5 * time.Second)
	var client *daemon.Client
	for {
		client, err = launcher().NewClient(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = client.Close() })

	// A read-only resolver over the daemon's own DB, mirroring buildServer's
	// subject wiring, so the test can pull the exact subject.Subject the gate
	// sees and evaluate interaction.Gate() against it directly.
	resolver := subject.Resolver{
		Store: store.NewSubjectStore(s.DB()),
	}
	return &e2e{t: t, client: client, resolver: resolver}
}

func (e *e2e) do(env daemon.Envelope) daemon.Reply {
	e.t.Helper()
	r, err := e.client.Do(context.Background(), env)
	if err != nil {
		e.t.Fatalf("Do(%s): %v", env.Op, err)
	}
	return r
}

func (e *e2e) agentEnv(op daemon.Op, body any) daemon.Envelope {
	e.t.Helper()
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	return daemon.Envelope{Op: op, Session: e2eSession, ClaudePID: e2ePID, Scope: e2eScope, Body: raw}
}

func (e *e2e) start() string {
	e.t.Helper()
	r := e.do(e.agentEnv(interaction.OpStart, nil))
	if !r.OK {
		e.t.Fatalf("start: %s", r.Error)
	}
	if r.SubjectID == "" {
		e.t.Fatal("start returned an empty subject id")
	}
	return r.SubjectID
}

func (e *e2e) ask(q interaction.QuestionPayload) (string, int64) {
	e.t.Helper()
	r := e.do(e.agentEnv(interaction.OpAsk, q))
	if !r.OK {
		e.t.Fatalf("ask: %s", r.Error)
	}
	var reply struct {
		SubjectID  string `json:"subject_id"`
		QuestionID int64  `json:"question_id"`
	}
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		e.t.Fatalf("unmarshal ask reply: %v", err)
	}
	return reply.SubjectID, reply.QuestionID
}

// answer submits the human's pick over the answerer envelope, which carries
// neither the agent session nor pid — exactly as the TUI/answer command does —
// and returns whether the subject idled.
func (e *e2e) answer(subjectID string, questionID int64, selected []string) bool {
	e.t.Helper()
	body, _ := json.Marshal(interaction.AnswerPayload{
		SubjectID:  subjectID,
		QuestionID: questionID,
		Selected:   selected,
	})
	r := e.do(daemon.Envelope{Op: interaction.OpAnswer, Scope: e2eScope, Body: body})
	if !r.OK {
		e.t.Fatalf("answer: %s", r.Error)
	}
	var reply struct {
		Idled bool `json:"idled"`
	}
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		e.t.Fatalf("unmarshal answer reply: %v", err)
	}
	return reply.Idled
}

// resolveSubject reads the exact subject the gate sees for this agent window,
// through the same read-only resolver path the daemon's guard-edit uses.
func (e *e2e) resolveSubject() subject.Subject {
	e.t.Helper()
	w := subject.Window{Session: e2eSession, ClaudePID: e2ePID}
	sub, ok, err := e.resolver.Find(context.Background(), w, e2eScope)
	if err != nil {
		e.t.Fatalf("resolve subject: %v", err)
	}
	if !ok {
		e.t.Fatal("no subject resolved for the agent window")
	}
	return sub
}

// gateDirect evaluates the production interaction.Gate() against the freshly
// resolved subject — the gate sees only the subject, so this is the same verdict
// the daemon would render.
func (e *e2e) gateDirect() (bool, string) {
	e.t.Helper()
	return interaction.Gate()(context.Background(), e.resolveSubject(), daemon.ToolCall{
		Name:  "Edit",
		Input: json.RawMessage(`{"file_path":"x.go"}`),
	})
}

// gateGuardEdit drives the gate end-to-end through the daemon's guard-edit op,
// which resolves the subject server-side and runs the wired interaction.Gate().
func (e *e2e) gateGuardEdit() (bool, string) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"tool_name":  "Edit",
		"tool_input": json.RawMessage(`{"file_path":"x.go"}`),
	})
	r := e.do(daemon.Envelope{
		Op: daemon.OpGuardEdit, Session: e2eSession, ClaudePID: e2ePID, Scope: e2eScope, Body: body,
	})
	if !r.OK {
		e.t.Fatalf("guard-edit: %s", r.Error)
	}
	return r.Allow, r.Reason
}

func (e *e2e) status(subjectID string) string {
	e.t.Helper()
	r := e.do(daemon.Envelope{Op: interaction.OpList, Scope: e2eScope})
	if !r.OK {
		e.t.Fatalf("list: %s", r.Error)
	}
	var reply struct {
		Subjects []interaction.ListedSubject `json:"subjects"`
	}
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		e.t.Fatalf("unmarshal list: %v", err)
	}
	for _, s := range reply.Subjects {
		if s.SubjectID == subjectID {
			return s.Status
		}
	}
	return interaction.StatusClosed
}

func (e *e2e) poll(subjectID string, questionID int64) (bool, interaction.AnswerPayload) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"subject_id": subjectID, "question_id": questionID})
	r := e.do(daemon.Envelope{Op: interaction.OpAnswerPoll, Scope: e2eScope, Body: body})
	if !r.OK {
		e.t.Fatalf("answer-poll: %s", r.Error)
	}
	var reply struct {
		Answered bool            `json:"answered"`
		Answer   json.RawMessage `json:"answer,omitempty"`
	}
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		e.t.Fatalf("unmarshal poll: %v", err)
	}
	if !reply.Answered {
		if len(reply.Answer) != 0 {
			e.t.Fatalf("poll reported not-answered but carried an answer body %s", reply.Answer)
		}
		return false, interaction.AnswerPayload{}
	}
	var ans interaction.AnswerPayload
	if err := json.Unmarshal(reply.Answer, &ans); err != nil {
		e.t.Fatalf("unmarshal poll answer: %v", err)
	}
	return true, ans
}

func e2eQuestion(header string) interaction.QuestionPayload {
	return interaction.QuestionPayload{
		Header:  header,
		Prompt:  "pick one",
		Options: []interaction.Option{{Label: "yes"}, {Label: "no"}},
	}
}

// assertBlocks asserts both the direct gate and the end-to-end guard-edit op
// block the edit with the exact awaiting reason.
func assertBlocks(t *testing.T, e *e2e, where string) {
	t.Helper()
	if allow, reason := e.gateDirect(); allow || reason != gateBlockReasonValue() {
		t.Fatalf("%s: direct gate = (%v, %q), want blocked with the awaiting reason", where, allow, reason)
	}
	if allow, reason := e.gateGuardEdit(); allow || reason == "" {
		t.Fatalf("%s: guard-edit gate = (%v, %q), want blocked with a reason", where, allow, reason)
	}
}

// assertAllows asserts both the direct gate and the end-to-end guard-edit op
// permit the edit with no reason.
func assertAllows(t *testing.T, e *e2e, where string) {
	t.Helper()
	if allow, reason := e.gateDirect(); !allow || reason != "" {
		t.Fatalf("%s: direct gate = (%v, %q), want allowed with no reason", where, allow, reason)
	}
	if allow, reason := e.gateGuardEdit(); !allow || reason != "" {
		t.Fatalf("%s: guard-edit gate = (%v, %q), want allowed with no reason", where, allow, reason)
	}
}

// gateBlockReasonValue is the exact awaiting reason interaction.Gate() returns;
// pulled from the gate itself so the assertion can't drift from production.
func gateBlockReasonValue() string {
	_, reason := interaction.Gate()(context.Background(),
		subject.Subject{Status: interaction.StatusAwaiting}, daemon.ToolCall{})
	return reason
}

// TestE2EAskGateAnswerRoundTrip is the phase-completion gate: it drives the real
// daemon through the full ask → block → answer → release → poll cycle with no
// nested claude, asserting exact statuses and the exact answer payload.
func TestE2EAskGateAnswerRoundTrip(t *testing.T) {
	e := newE2E(t)

	// Agent starts the scope's subject; a fresh subject is idle and editable.
	subjectID := e.start()
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after start = %q, want %q", got, interaction.StatusIdle)
	}
	assertAllows(t, e, "after start")

	// Agent asks; the same subject flips to awaiting on the first event seq.
	askSubject, questionID := e.ask(e2eQuestion("deploy?"))
	if askSubject != subjectID {
		t.Fatalf("ask resolved subject %q, want the started subject %q", askSubject, subjectID)
	}
	if questionID != 1 {
		t.Fatalf("question_id = %d, want 1 (the first per-subject event seq)", questionID)
	}
	if got := e.status(subjectID); got != interaction.StatusAwaiting {
		t.Fatalf("status after ask = %q, want %q", got, interaction.StatusAwaiting)
	}

	// 3. The gate BLOCKS against the now-awaiting subject.
	assertBlocks(t, e, "while awaiting")

	// 4. The poll reports not-answered while the question is open.
	if answered, _ := e.poll(subjectID, questionID); answered {
		t.Fatal("poll reported answered while the question is still open")
	}

	// 5. The human submits the answer.
	selected := []string{"yes"}
	idled := e.answer(subjectID, questionID, selected)
	if !idled {
		t.Fatal("answering the only open question must idle the subject")
	}
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after answer = %q, want %q", got, interaction.StatusIdle)
	}

	// 6. The gate RELEASES once the subject idles.
	assertAllows(t, e, "after answer")

	// 7. The poll now reports answered, carrying the exact submitted payload.
	answered, ans := e.poll(subjectID, questionID)
	if !answered {
		t.Fatal("poll reported not-answered after the answer was submitted")
	}
	if ans.SubjectID != subjectID {
		t.Fatalf("poll answer subject_id = %q, want %q", ans.SubjectID, subjectID)
	}
	if ans.QuestionID != questionID {
		t.Fatalf("poll answer question_id = %d, want %d", ans.QuestionID, questionID)
	}
	if len(ans.Selected) != 1 || ans.Selected[0] != "yes" {
		t.Fatalf("poll answer selected = %v, want [yes]", ans.Selected)
	}
	if ans.Other != "" || ans.Notes != "" {
		t.Fatalf("poll answer carried other=%q notes=%q, want both empty", ans.Other, ans.Notes)
	}
}

// TestE2EMultiAskGateHoldsUntilAllAnswered asserts the gate stays closed until
// every open question is answered: two open, answering the first leaves the
// subject awaiting; answering the second idles it.
func TestE2EMultiAskGateHoldsUntilAllAnswered(t *testing.T) {
	e := newE2E(t)

	subjectID, q1 := e.ask(e2eQuestion("q1"))
	askSubject2, q2 := e.ask(e2eQuestion("q2"))
	if askSubject2 != subjectID {
		t.Fatalf("second ask resolved subject %q, want %q", askSubject2, subjectID)
	}
	if q1 != 1 || q2 != 2 {
		t.Fatalf("question ids = (%d, %d), want (1, 2)", q1, q2)
	}
	if got := e.status(subjectID); got != interaction.StatusAwaiting {
		t.Fatalf("status with two open = %q, want %q", got, interaction.StatusAwaiting)
	}
	assertBlocks(t, e, "with two open questions")

	// Answering q1 must NOT idle the subject while q2 is still open.
	if idled := e.answer(subjectID, q1, []string{"yes"}); idled {
		t.Fatal("answering q1 idled the subject while q2 was still open")
	}
	if got := e.status(subjectID); got != interaction.StatusAwaiting {
		t.Fatalf("status after answering q1 = %q, want still %q", got, interaction.StatusAwaiting)
	}
	assertBlocks(t, e, "after answering q1, q2 still open")

	// q1 reads answered, q2 still open.
	if answered, _ := e.poll(subjectID, q1); !answered {
		t.Fatal("poll on q1 reported not-answered after it was answered")
	}
	if answered, _ := e.poll(subjectID, q2); answered {
		t.Fatal("poll on q2 reported answered while it was still open")
	}

	// Answering q2 — the last open question — idles the subject and releases.
	if idled := e.answer(subjectID, q2, []string{"no"}); !idled {
		t.Fatal("answering the last open question must idle the subject")
	}
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after answering q2 = %q, want %q", got, interaction.StatusIdle)
	}
	assertAllows(t, e, "after answering both questions")
}

// TestE2ECaptureNotificationNeverBlocks asserts a captured notification on an
// idle subject is a pure log append: the subject stays idle and the gate keeps
// allowing edits.
func TestE2ECaptureNotificationNeverBlocks(t *testing.T) {
	e := newE2E(t)

	subjectID := e.start()
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after start = %q, want %q", got, interaction.StatusIdle)
	}

	body, _ := json.Marshal(interaction.NotificationPayload{Message: "build finished"})
	r := e.do(daemon.Envelope{
		Op: interaction.OpCaptureNotification, Session: e2eSession, ClaudePID: e2ePID, Scope: e2eScope, Body: body,
	})
	if !r.OK {
		t.Fatalf("capture-notification: %s", r.Error)
	}
	if r.SubjectID != subjectID {
		t.Fatalf("capture-notification resolved subject %q, want %q", r.SubjectID, subjectID)
	}

	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after capture-notification = %q, want unchanged %q", got, interaction.StatusIdle)
	}
	assertAllows(t, e, "after capture-notification on an idle subject")
}

// pending reads the subject's still-open questions via OpPending — the same
// projection the TUI reseeds from and the gate's open-count keys off — so the
// test can assert a captured native question never lands in the answerable queue.
func (e *e2e) pending(subjectID string) []interaction.PendingQuestion {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"subject_id": subjectID})
	r := e.do(daemon.Envelope{Op: interaction.OpPending, Scope: e2eScope, Body: body})
	if !r.OK {
		e.t.Fatalf("pending: %s", r.Error)
	}
	var reply struct {
		Questions []interaction.PendingQuestion `json:"questions"`
	}
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		e.t.Fatalf("unmarshal pending: %v", err)
	}
	return reply.Questions
}

// TestE2ECaptureQuestionNeverBlocks asserts a captured native AskUserQuestion on
// an idle subject is a pure system-origin log append: the subject stays idle
// (NOT awaiting), the gate keeps allowing edits, and — critically — it never
// projects a pending_questions row, so it never becomes answerable or countable
// in the TUI. It is a mirror for visibility, not an open cc-runtime question.
func TestE2ECaptureQuestionNeverBlocks(t *testing.T) {
	e := newE2E(t)

	subjectID := e.start()
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after start = %q, want %q", got, interaction.StatusIdle)
	}
	if open := e.pending(subjectID); len(open) != 0 {
		t.Fatalf("a fresh subject has %d pending questions, want 0", len(open))
	}

	body, _ := json.Marshal(interaction.QuestionPayload{
		Header:      "Approach",
		Prompt:      "which deploy strategy?",
		MultiSelect: false,
		Options:     []interaction.Option{{Label: "blue-green"}, {Label: "canary"}},
	})
	r := e.do(daemon.Envelope{
		Op: interaction.OpCaptureQuestion, Session: e2eSession, ClaudePID: e2ePID, Scope: e2eScope, Body: body,
	})
	if !r.OK {
		t.Fatalf("capture-question: %s", r.Error)
	}
	if r.SubjectID != subjectID {
		t.Fatalf("capture-question resolved subject %q, want %q", r.SubjectID, subjectID)
	}

	// The capture is a mirror, never an open question: status is unchanged.
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after capture-question = %q, want unchanged %q", got, interaction.StatusIdle)
	}
	// No pending row: the question is not answerable and does not inflate the
	// open-question count the TUI reseeds from.
	if open := e.pending(subjectID); len(open) != 0 {
		t.Fatalf("capture-question projected %d pending questions, want 0 (it is a mirror, not an open question)", len(open))
	}
	// The gate still allows: a mirror never engages the edit gate.
	assertAllows(t, e, "after capture-question on an idle subject")
}
