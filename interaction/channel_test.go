package interaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/consume"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
)

// channelHarness runs a real daemon on cc-runtime's production state-dir layout
// (AppPaths), so the ask tool's internal client — which dials
// AppPaths().SocketPath() — connects to it. HOME is redirected to a short temp
// dir so the socket path stays under the sun_path limit.
type channelHarness struct {
	t      *testing.T
	client *daemon.Client
	port   int
}

func newChannelHarness(t *testing.T) *channelHarness {
	t.Helper()
	t.Setenv("HOME", shortTempHome(t))
	p := AppPaths()

	s, err := daemon.New(daemon.Config{
		AppName:         AppName,
		Paths:           p,
		Version:         "v0.0.0-test",
		ActiveStatuses:  ActiveStatuses,
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

	r, err := client.Do(context.Background(), daemon.Envelope{Op: OpList, Scope: testScope})
	if err != nil {
		t.Fatalf("list for http port: %v", err)
	}
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	return &channelHarness{t: t, client: client, port: lr.HTTPPort}
}

func askTool(t *testing.T, session string, pid int) channel.Tool {
	t.Helper()
	tools, method, instructions, err := ChannelTools(context.Background(), session, testScope, pid)
	if err != nil {
		t.Fatalf("ChannelTools: %v", err)
	}
	if method != notifyMethod {
		t.Fatalf("notify method = %q, want %q", method, notifyMethod)
	}
	if !strings.Contains(instructions, "AskUserQuestion") {
		t.Fatalf("instructions missing AskUserQuestion steer: %q", instructions)
	}
	for _, tl := range tools {
		if tl.Name == "ask" {
			return tl
		}
	}
	t.Fatal("ask tool not advertised")
	return channel.Tool{}
}

func submitAnswerFor(t *testing.T, client *daemon.Client, selected ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, err := client.Do(context.Background(), daemon.Envelope{Op: OpList, Scope: testScope})
		if err != nil {
			t.Errorf("list awaiting: %v", err)
			return
		}
		var lr listReply
		if err := json.Unmarshal(r.Body, &lr); err != nil {
			t.Errorf("unmarshal list: %v", err)
			return
		}
		for _, s := range lr.Subjects {
			if s.Status != StatusAwaiting {
				continue
			}
			pb, _ := json.Marshal(subjectBody{SubjectID: s.SubjectID})
			pr := mustDo(t, client, daemon.Envelope{Op: OpPending, Scope: testScope, Body: pb})
			var pend pendingReply
			if err := json.Unmarshal(pr.Body, &pend); err != nil {
				t.Errorf("unmarshal pending: %v", err)
				return
			}
			if len(pend.Questions) == 0 {
				continue
			}
			ab, _ := json.Marshal(AnswerPayload{SubjectID: s.SubjectID, QuestionID: pend.Questions[0].QuestionID, Selected: selected})
			mustDo(t, client, daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: ab})
			return
		}
		if time.Now().After(deadline) {
			t.Error("no awaiting question appeared to answer")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustDo(t *testing.T, client *daemon.Client, env daemon.Envelope) daemon.Reply {
	t.Helper()
	r, err := client.Do(context.Background(), env)
	if err != nil {
		t.Fatalf("Do(%s): %v", env.Op, err)
	}
	if !r.OK {
		t.Fatalf("Do(%s): %s", env.Op, r.Error)
	}
	return r
}

func sampleArgs(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(QuestionPayload{
		Header:  "approach",
		Prompt:  "pick one",
		Options: []Option{{Label: "ship"}, {Label: "hold"}},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

func TestAskToolLongPollReturnsHumanAnswer(t *testing.T) {
	h := newChannelHarness(t)
	ask := askTool(t, testSession, testPID)

	go submitAnswerFor(t, h.client, "ship")

	text, isErr := ask.Handler(context.Background(), sampleArgs(t))
	if isErr {
		t.Fatalf("ask handler returned error: %q", text)
	}
	if !strings.Contains(text, "ship") {
		t.Fatalf("ask result = %q, want the human's selection 'ship'", text)
	}
}

func TestAskToolDegradesToPendingWithoutError(t *testing.T) {
	h := newChannelHarness(t)

	// Drive a question directly so the inline long-poll has a target, then let an
	// injected budget expire with no answer ever arriving.
	r := mustDo(t, h.client, daemon.Envelope{Op: OpAsk, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: sampleArgs(t)})
	var ar askReply
	if err := json.Unmarshal(r.Body, &ar); err != nil {
		t.Fatalf("unmarshal ask reply: %v", err)
	}

	text, err := awaitAnswer(context.Background(), h.client, testSession, testScope, testPID,
		pollBody{SubjectID: ar.SubjectID, QuestionID: ar.QuestionID}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitAnswer errored on budget expiry: %v", err)
	}
	if text != "" {
		t.Fatalf("awaitAnswer = %q on budget expiry, want empty so the handler renders the non-error pending string", text)
	}
}

func TestHumanAnswerNotSuppressedByExcludeAgentOrigin(t *testing.T) {
	h := newChannelHarness(t)

	r := mustDo(t, h.client, daemon.Envelope{Op: OpAsk, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: sampleArgs(t)})
	var ar askReply
	if err := json.Unmarshal(r.Body, &ar); err != nil {
		t.Fatalf("unmarshal ask reply: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gotAnswer := make(chan string, 1)
	go func() {
		_ = consume.ConsumeEvents(ctx, consume.StreamSource{
			Port:          h.port,
			SubjectID:     ar.SubjectID,
			Consumer:      "test-watch",
			ExcludeOrigin: event.OriginAgent,
			Paths:         AppPaths(),
		}, func(_ int64, data string) (bool, error) {
			var e struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(data), &e)
			if e.Type == EventAnswer {
				gotAnswer <- data
				return true, nil
			}
			return false, nil
		})
	}()

	ab, _ := json.Marshal(AnswerPayload{SubjectID: ar.SubjectID, QuestionID: ar.QuestionID, Selected: []string{"hold"}})
	mustDo(t, h.client, daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: ab})

	select {
	case data := <-gotAnswer:
		if !strings.Contains(data, "hold") {
			t.Fatalf("streamed answer = %q, want the human's 'hold' selection", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OriginHuman answer was suppressed by ExcludeOrigin=OriginAgent; it must stream through")
	}
}

// subjectClaudePID reads a subject's window pid straight from the daemon's
// SQLite, the source of truth the resolver binds against. A second read-only
// connection sees committed rows under WAL.
func subjectClaudePID(t *testing.T, subjectID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", AppPaths().DBPath()+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open daemon db: %v", err)
	}
	defer db.Close()
	var pid int
	if err := db.QueryRow(`SELECT claude_pid FROM subjects WHERE id=?`, subjectID).Scan(&pid); err != nil {
		t.Fatalf("read claude_pid for subject %s: %v", subjectID, err)
	}
	return pid
}

// awaitingSubjectID polls the scope until exactly one subject is awaiting and
// returns its id, so a test can inspect the subject the real ask handler created.
func awaitingSubjectID(t *testing.T, client *daemon.Client, scope string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := mustDo(t, client, daemon.Envelope{Op: OpList, Scope: scope})
		var lr listReply
		if err := json.Unmarshal(r.Body, &lr); err != nil {
			t.Fatalf("unmarshal list: %v", err)
		}
		for _, s := range lr.Subjects {
			if s.Status == StatusAwaiting {
				return s.SubjectID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no awaiting subject appeared in scope %q", scope)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAskToolSubjectKeyedToClaudePID drives the real ask tool with a claude
// window pid distinct from the channel/test process's own getpid, then asserts
// the subject the daemon created binds to the passed-in claude pid — not the
// channel server's getpid. Before the fix (ChannelTools stamped os.Getpid),
// the subject bound to the test process and this assertion would fail.
func TestAskToolSubjectKeyedToClaudePID(t *testing.T) {
	const claudePID = 991337 // a fabricated claude window pid, never == os.Getpid()
	h := newChannelHarness(t)
	ask := askTool(t, testSession, claudePID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ask.Handler(context.Background(), sampleArgs(t))
	}()

	subjectID := awaitingSubjectID(t, h.client, testScope)
	if got := subjectClaudePID(t, subjectID); got != claudePID {
		t.Fatalf("subject %s claude_pid = %d, want the passed-in claude window pid %d (not the channel server's getpid)", subjectID, got, claudePID)
	}

	// Unblock the handler goroutine so teardown is clean.
	submitAnswerFor(t, h.client, "ship")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ask handler did not return after the answer was submitted")
	}
}

// TestNoCrossWindowTheftWhileOwnerLives proves the window-ownership invariant:
// a second window B in the same scope must NOT adopt window A's awaiting subject.
// Ownership is per-window (keyed to the claude pid), and the resolver never
// adopts another window's subject, so B's start creates its own subject. Before
// the fix, ChannelTools stamped the channel server's getpid, so A's subject bound
// to the wrong window and B stole A's awaiting subject and its open question.
func TestNoCrossWindowTheftWhileOwnerLives(t *testing.T) {
	const (
		pidA       = 700001
		pidB       = 700002
		sessionA   = "sess-A"
		sessionB   = "sess-B"
		theftScope = "/scope/theft"
	)
	h := newChannelHarness(t)

	// Window A asks through the real handler; its subject goes awaiting, keyed to pidA.
	askA := askToolFor(t, sessionA, pidA, theftScope)
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		askA.Handler(context.Background(), sampleArgs(t))
	}()
	subjectA := awaitingSubjectID(t, h.client, theftScope)
	if got := subjectClaudePID(t, subjectA); got != pidA {
		t.Fatalf("A's subject claude_pid = %d, want %d", got, pidA)
	}

	// Window B (different claude pid, same scope) starts while A's owner is alive.
	rB := mustDo(t, h.client, daemon.Envelope{Op: OpStart, Session: sessionB, ClaudePID: pidB, Scope: theftScope})
	if rB.SubjectID == subjectA {
		t.Fatalf("window B adopted A's awaiting subject %s while A's owner (pid %d) is alive", subjectA, pidA)
	}

	// A's subject is untouched: still keyed to pidA and still awaiting.
	if got := subjectClaudePID(t, subjectA); got != pidA {
		t.Fatalf("after B started, A's subject claude_pid = %d, want it untouched at %d", got, pidA)
	}
	if got := subjectStatus(t, h.client, theftScope, subjectA); got != StatusAwaiting {
		t.Fatalf("after B started, A's subject status = %q, want %q (its open question must remain)", got, StatusAwaiting)
	}

	// Unblock A's handler goroutine for clean teardown.
	answerSubject(t, h.client, theftScope, subjectA, "ship")
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("A's ask handler did not return after its answer was submitted")
	}
}

// TestFreshWindowDoesNotInheritDeadOwnersAwaitingSubject proves the cross-window
// adoption invariant: an awaiting subject is never adopted by another window.
// Window A asks (subject goes awaiting, keyed to pidA). A fresh window B —
// different pid, different session, same scope — issues OpStart and must get its
// own NEW, IDLE subject rather than inheriting A's awaiting one and its
// open-question edit-block. Ownership is per-window: the resolver binds B to its
// own pid-latest subject (none yet), so B creates fresh and A's subject is left
// untouched.
func TestFreshWindowDoesNotInheritDeadOwnersAwaitingSubject(t *testing.T) {
	const (
		pidA      = 800001
		pidB      = 800002
		sessionA  = "sess-A"
		sessionB  = "sess-B"
		deadScope = "/scope/dead-owner"
	)
	h := newChannelHarness(t)

	// Window A asks through the real handler; its subject goes awaiting, keyed to pidA.
	askA := askToolFor(t, sessionA, pidA, deadScope)
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		askA.Handler(context.Background(), sampleArgs(t))
	}()
	subjectA := awaitingSubjectID(t, h.client, deadScope)
	if got := subjectClaudePID(t, subjectA); got != pidA {
		t.Fatalf("A's subject claude_pid = %d, want %d", got, pidA)
	}

	// Window B (different claude pid, different session, same scope) starts.
	rB := mustDo(t, h.client, daemon.Envelope{Op: OpStart, Session: sessionB, ClaudePID: pidB, Scope: deadScope})
	if rB.SubjectID == "" {
		t.Fatal("window B's start returned no subject id")
	}
	if rB.SubjectID == subjectA {
		t.Fatalf("window B adopted dead window A's awaiting subject %s; a fresh window must not inherit a stale open-question block", subjectA)
	}
	if got := subjectStatus(t, h.client, deadScope, rB.SubjectID); got != StatusIdle {
		t.Fatalf("window B's subject status = %q, want %q (a fresh window starts editable)", got, StatusIdle)
	}

	// A's awaiting subject is untouched: its open question still stands, answerable
	// by subject_id+question_id (and visible in the TUI listing).
	if got := subjectStatus(t, h.client, deadScope, subjectA); got != StatusAwaiting {
		t.Fatalf("after B started, A's subject status = %q, want %q (its open question must remain answerable)", got, StatusAwaiting)
	}

	// Unblock A's handler goroutine for clean teardown.
	answerSubject(t, h.client, deadScope, subjectA, "ship")
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("A's ask handler did not return after its answer was submitted")
	}
}

// askToolFor is askTool with an explicit scope, for tests that drive more than
// one window in a non-default scope.
func askToolFor(t *testing.T, session string, pid int, scope string) channel.Tool {
	t.Helper()
	tools, _, _, err := ChannelTools(context.Background(), session, scope, pid)
	if err != nil {
		t.Fatalf("ChannelTools: %v", err)
	}
	for _, tl := range tools {
		if tl.Name == "ask" {
			return tl
		}
	}
	t.Fatal("ask tool not advertised")
	return channel.Tool{}
}

// subjectStatus returns a named subject's status from the scope listing.
func subjectStatus(t *testing.T, client *daemon.Client, scope, subjectID string) string {
	t.Helper()
	r := mustDo(t, client, daemon.Envelope{Op: OpList, Scope: scope})
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, s := range lr.Subjects {
		if s.SubjectID == subjectID {
			return s.Status
		}
	}
	t.Fatalf("subject %s not found in scope %q", subjectID, scope)
	return ""
}

// answerSubject answers the named subject's first open question in the scope.
func answerSubject(t *testing.T, client *daemon.Client, scope, subjectID, selected string) {
	t.Helper()
	pb, _ := json.Marshal(subjectBody{SubjectID: subjectID})
	pr := mustDo(t, client, daemon.Envelope{Op: OpPending, Scope: scope, Body: pb})
	var pend pendingReply
	if err := json.Unmarshal(pr.Body, &pend); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if len(pend.Questions) == 0 {
		t.Fatalf("subject %s has no open question to answer", subjectID)
	}
	ab, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: pend.Questions[0].QuestionID, Selected: []string{selected}})
	mustDo(t, client, daemon.Envelope{Op: OpAnswer, Scope: scope, Body: ab})
}
