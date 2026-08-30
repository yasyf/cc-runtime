package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yasyf/daemonkit"

	"github.com/yasyf/cc-runtime/interaction"
)

// showsError reports whether render() short-circuited to the fatal error screen,
// so a test can assert the TUI recovered (error screen gone) after a later
// successful transition.
func showsError(m Model) bool {
	return strings.Contains(m.render(), "error: ")
}

// questionFrame builds a single-option question event frame for the given seq.
func questionFrame(t *testing.T, seq int64, prompt string) liveEvent {
	t.Helper()
	q := interaction.QuestionPayload{Prompt: prompt, Options: []interaction.Option{{Label: "A"}}}
	return liveEvent{Seq: seq, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)}
}

// frame builds the streamed event frame the SSE plane delivers: the bare payload
// with a `type` discriminator merged in, exactly as wireEvent produces.
func frame(t *testing.T, typ string, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	m["type"] = typ
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return string(out)
}

// update applies one message through the real Update path and returns the model,
// re-asserted to the concrete type so the test drives production code end to end.
func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return out
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// newTestModel builds a model already resolved to a subject, so the test can feed
// question events without standing up the OpList poll. The socket client is nil:
// the pure Update + submit path never touches it.
func newTestModel(subjectID string) Model {
	m := NewModel("/repo", nil, make(chan liveEvent))
	m.resolved = true
	m.res = resolution{SubjectID: subjectID, HTTPPort: 4321}
	return m
}

func TestTransientDaemonSwap(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "no listener", err: daemonkit.ErrAbsent, want: true},
		{name: "still starting", err: daemonkit.ErrNotReady, want: true},
		{name: "already draining", err: daemonkit.ErrDraining, want: true},
		{name: "wrapped no listener", err: fmt.Errorf("resolve: %w", daemonkit.ErrAbsent), want: true},
		{name: "untrusted peer", err: daemonkit.ErrUntrusted},
		{name: "ordinary error", err: errors.New("boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transientDaemonSwap(tt.err); got != tt.want {
				t.Fatalf("transientDaemonSwap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateStoresQuestionEvent(t *testing.T) {
	m := newTestModel("subj-1")
	q := interaction.QuestionPayload{
		Header: "Approach",
		Prompt: "Which database?",
		Options: []interaction.Option{
			{Label: "Postgres"},
			{Label: "SQLite"},
		},
	}
	m = update(t, m, liveEvent{Seq: 7, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)})

	if len(m.questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(m.questions))
	}
	got := m.questions[0]
	if got.ID != 7 {
		t.Fatalf("question id = %d, want 7", got.ID)
	}
	if got.Payload.Prompt != "Which database?" {
		t.Fatalf("prompt = %q, want %q", got.Payload.Prompt, "Which database?")
	}
	if len(got.Payload.Options) != 2 {
		t.Fatalf("options = %d, want 2", len(got.Payload.Options))
	}
	if _, ok := m.focused(); !ok {
		t.Fatal("focus did not advance to the new question")
	}
	if m.openCount() != 1 {
		t.Fatalf("openCount = %d, want 1", m.openCount())
	}
}

func TestUpdateDeduplicatesQuestion(t *testing.T) {
	m := newTestModel("subj-1")
	q := interaction.QuestionPayload{Prompt: "Pick one", Options: []interaction.Option{{Label: "A"}}}
	data := frame(t, interaction.EventQuestion, q)
	m = update(t, m, liveEvent{Seq: 3, Type: interaction.EventQuestion, Data: data})
	m = update(t, m, liveEvent{Seq: 3, Type: interaction.EventQuestion, Data: data})
	if len(m.questions) != 1 {
		t.Fatalf("re-delivered question duplicated: %d questions, want 1", len(m.questions))
	}
}

func TestSubmitBuildsSingleSelectAnswer(t *testing.T) {
	m := newTestModel("subj-42")
	q := interaction.QuestionPayload{
		Prompt: "Which approach?",
		Options: []interaction.Option{
			{Label: "Refactor"},
			{Label: "Rewrite"},
			{Label: "Patch"},
		},
	}
	m = update(t, m, liveEvent{Seq: 99, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)})

	// Move the cursor down to "Rewrite" and select it.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})

	// Enter the notes field and type a note.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = update(t, m, keyRunes("ship it"))

	a, ok := m.submit()
	if !ok {
		t.Fatal("submit returned no payload for a focused question")
	}
	if a.SubjectID != "subj-42" {
		t.Fatalf("SubjectID = %q, want %q", a.SubjectID, "subj-42")
	}
	if a.QuestionID != 99 {
		t.Fatalf("QuestionID = %d, want 99", a.QuestionID)
	}
	if len(a.Selected) != 1 || a.Selected[0] != "Rewrite" {
		t.Fatalf("Selected = %v, want [Rewrite]", a.Selected)
	}
	if a.Notes != "ship it" {
		t.Fatalf("Notes = %q, want %q", a.Notes, "ship it")
	}
}

func TestSubmitSingleSelectReplacesPriorChoice(t *testing.T) {
	m := newTestModel("subj-1")
	q := interaction.QuestionPayload{
		Prompt:  "Pick one",
		Options: []interaction.Option{{Label: "A"}, {Label: "B"}},
	}
	m = update(t, m, liveEvent{Seq: 1, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)})

	// Select A, then move to B and select it: single-select keeps only B.
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})

	a, _ := m.submit()
	if len(a.Selected) != 1 || a.Selected[0] != "B" {
		t.Fatalf("single-select left %v, want [B]", a.Selected)
	}
}

func TestSubmitBuildsMultiSelectAnswer(t *testing.T) {
	m := newTestModel("subj-7")
	q := interaction.QuestionPayload{
		Prompt:      "Which checks?",
		MultiSelect: true,
		Options: []interaction.Option{
			{Label: "lint"},
			{Label: "test"},
			{Label: "vet"},
		},
	}
	m = update(t, m, liveEvent{Seq: 5, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)})

	// Select lint (cursor 0), then move to vet (cursor 2) and select it too.
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})

	a, _ := m.submit()
	// Selected preserves option order, so [lint, vet].
	if len(a.Selected) != 2 || a.Selected[0] != "lint" || a.Selected[1] != "vet" {
		t.Fatalf("Selected = %v, want [lint vet]", a.Selected)
	}
}

func TestUpdateAppendsNotificationToFeed(t *testing.T) {
	m := newTestModel("subj-1")
	n := interaction.NotificationPayload{Message: "build is green", Urgency: "low"}
	m = update(t, m, liveEvent{Seq: 2, Type: interaction.EventNotification, Data: frame(t, interaction.EventNotification, n)})

	if len(m.feed) != 1 {
		t.Fatalf("feed = %d, want 1", len(m.feed))
	}
	if m.feed[0].Message != "build is green" {
		t.Fatalf("feed message = %q, want %q", m.feed[0].Message, "build is green")
	}
}

func TestSubmittedAdvancesToNextQuestion(t *testing.T) {
	m := newTestModel("subj-1")
	first := interaction.QuestionPayload{Prompt: "Q1", Options: []interaction.Option{{Label: "A"}}}
	second := interaction.QuestionPayload{Prompt: "Q2", Options: []interaction.Option{{Label: "B"}}}
	m = update(t, m, liveEvent{Seq: 1, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, first)})
	m = update(t, m, liveEvent{Seq: 2, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, second)})

	if q, _ := m.focused(); q.ID != 1 {
		t.Fatalf("initial focus = %d, want 1", q.ID)
	}
	m = update(t, m, submittedMsg{QuestionID: 1})
	q, ok := m.focused()
	if !ok || q.ID != 2 {
		t.Fatalf("focus after answering Q1 = %d (ok=%v), want 2", q.ID, ok)
	}
	if m.openCount() != 1 {
		t.Fatalf("openCount = %d, want 1", m.openCount())
	}

	m = update(t, m, submittedMsg{QuestionID: 2})
	if _, ok := m.focused(); ok {
		t.Fatal("focus should be past the queue once all questions are answered")
	}
	if m.openCount() != 0 {
		t.Fatalf("openCount = %d, want 0", m.openCount())
	}
}

func TestPickAwaitingSelectsTheAwaitingSubject(t *testing.T) {
	lr := listReply{
		HTTPPort: 8080,
		Subjects: []interaction.ListedSubject{
			{SubjectID: "idle-one", Status: interaction.StatusIdle, Pending: 0},
			{SubjectID: "awaiting-one", Status: interaction.StatusAwaiting, Pending: 2},
		},
	}
	res, found, multiple := pickAwaiting(lr)
	if !found {
		t.Fatal("pickAwaiting did not find the awaiting subject")
	}
	if multiple {
		t.Fatal("pickAwaiting reported multiple with exactly one awaiting subject")
	}
	if res.SubjectID != "awaiting-one" {
		t.Fatalf("SubjectID = %q, want %q", res.SubjectID, "awaiting-one")
	}
	if res.HTTPPort != 8080 {
		t.Fatalf("HTTPPort = %d, want 8080", res.HTTPPort)
	}
}

func TestPickAwaitingNoneAwaiting(t *testing.T) {
	lr := listReply{
		HTTPPort: 8080,
		Subjects: []interaction.ListedSubject{
			{SubjectID: "idle-one", Status: interaction.StatusIdle},
		},
	}
	_, found, multiple := pickAwaiting(lr)
	if found {
		t.Fatal("pickAwaiting found a subject with none awaiting")
	}
	if multiple {
		t.Fatal("pickAwaiting reported multiple with none awaiting")
	}
}

// TestPickAwaitingMultipleIsTransient asserts that more than one awaiting subject
// is a non-latching poll state: pickAwaiting reports not-found but multiple=true
// (keep polling, surface a hint) rather than a fatal error, so a brief
// multi-awaiting window can't wedge the TUI.
func TestPickAwaitingMultipleIsTransient(t *testing.T) {
	lr := listReply{
		Subjects: []interaction.ListedSubject{
			{SubjectID: "a", Status: interaction.StatusAwaiting},
			{SubjectID: "b", Status: interaction.StatusAwaiting},
		},
	}
	_, found, multiple := pickAwaiting(lr)
	if found {
		t.Fatal("pickAwaiting should treat multiple awaiting subjects as transient (not-found), not resolve one")
	}
	if !multiple {
		t.Fatal("pickAwaiting should report multiple=true so the caller can surface the hint")
	}

	// Once the scope settles to a single awaiting subject, it resolves cleanly.
	settled := listReply{
		HTTPPort: 9090,
		Subjects: []interaction.ListedSubject{
			{SubjectID: "a", Status: interaction.StatusIdle},
			{SubjectID: "b", Status: interaction.StatusAwaiting},
		},
	}
	res, found, multiple := pickAwaiting(settled)
	if !found {
		t.Fatal("pickAwaiting did not attach once the scope settled to one awaiting subject")
	}
	if multiple {
		t.Fatal("pickAwaiting reported multiple after the scope settled to one")
	}
	if res.SubjectID != "b" {
		t.Fatalf("SubjectID = %q, want %q after settling", res.SubjectID, "b")
	}
}

// TestMultiAwaitingShowsNonLatchingHint asserts the model surfaces the informative
// multi-awaiting note (not the generic silent "waiting…") and that it is
// non-latching: a clearWaitNoteMsg from a settled poll removes it, and a
// resolvedMsg both clears it and never gets blocked by it. This proves the
// multiple-awaiting scope no longer leaves the TUI silently waiting forever.
func TestMultiAwaitingShowsNonLatchingHint(t *testing.T) {
	m := newTestModel("subj-1")
	m.resolved = false

	m = update(t, m, multiAwaitingMsg{})
	if m.waitNote != multiAwaitingNote {
		t.Fatalf("waitNote = %q, want the multi-awaiting note", m.waitNote)
	}
	if !strings.Contains(m.View(), multiAwaitingNote) {
		t.Fatal("View must show the informative multi-awaiting note while unresolved")
	}

	// A poll that settles to zero awaiting clears the note (non-latching).
	m = update(t, m, clearWaitNoteMsg{})
	if m.waitNote != "" {
		t.Fatalf("waitNote = %q, want cleared once the scope settles", m.waitNote)
	}

	// The note never blocks resolution: a resolvedMsg attaches and clears it.
	m = update(t, m, multiAwaitingMsg{})
	m = update(t, m, resolvedMsg{Res: resolution{SubjectID: "subj-1", HTTPPort: 1234}})
	if !m.resolved {
		t.Fatal("resolvedMsg must resolve even after a multi-awaiting hint")
	}
	if m.waitNote != "" {
		t.Fatalf("waitNote = %q, want cleared on resolve", m.waitNote)
	}
}

// TestErrMsgRecoversOnLiveEvent asserts a transient errMsg latched into m.err is
// cleared by the next successfully ingested live event, so a single bad frame or
// daemon swap can't brick the TUI: the error screen clears and the question is
// answerable. Before the latch fix this FAILS — m.err is never reset, so the
// error screen persists even after a good event arrives.
func TestErrMsgRecoversOnLiveEvent(t *testing.T) {
	m := newTestModel("subj-1")

	m = update(t, m, errMsg{Err: errors.New("daemon unavailable")})
	if !showsError(m) {
		t.Fatal("errMsg should latch into the error screen")
	}

	m = update(t, m, questionFrame(t, 7, "Which database?"))
	if showsError(m) {
		t.Fatal("a successfully ingested live event should clear the error screen")
	}
	q, ok := m.focused()
	if !ok || q.ID != 7 {
		t.Fatalf("focus after recovery = %d (ok=%v), want the new question 7", q.ID, ok)
	}
	if m.openCount() != 1 {
		t.Fatalf("openCount = %d, want 1 (question is answerable after recovery)", m.openCount())
	}
}

// TestSubmittedErrRecoversOnSuccess asserts a failed submit latches the error but
// the next successful submit clears it. Before the latch fix this FAILS: the
// error screen persists past the recovering submit.
func TestSubmittedErrRecoversOnSuccess(t *testing.T) {
	m := newTestModel("subj-1")
	m = update(t, m, questionFrame(t, 1, "Q1"))

	m = update(t, m, submittedMsg{QuestionID: 1, Err: errors.New("transient socket error")})
	if !showsError(m) {
		t.Fatal("a failed submit should latch into the error screen")
	}

	m = update(t, m, submittedMsg{QuestionID: 1})
	if showsError(m) {
		t.Fatal("a successful submit should clear the error screen")
	}
	if !m.answered[1] {
		t.Fatal("the recovering submit should mark the question answered")
	}
}

// TestResolvedMsgClearsError asserts re-resolution after a transient stream error
// clears the latch, so the live UI redraws instead of staying stuck on the error
// screen. Before the latch fix this FAILS.
func TestResolvedMsgClearsError(t *testing.T) {
	m := newTestModel("subj-1")

	m = update(t, m, liveEvent{Err: errors.New("stream terminated")})
	if !showsError(m) {
		t.Fatal("a terminal stream error should latch into the error screen")
	}

	m = update(t, m, resolvedMsg{Res: resolution{SubjectID: "subj-1", HTTPPort: 4321}})
	if showsError(m) {
		t.Fatal("a successful re-resolution should clear the error screen")
	}
}

// TestEnterIgnoredOnEmptyOptionsAnswer asserts enter with no selection and empty
// notes on an options question does not submit, so an accidental enter can't idle
// the subject with an empty answer. submitCmd is nil (no OpAnswer) in that case,
// and the question stays focused/open.
func TestEnterIgnoredOnEmptyOptionsAnswer(t *testing.T) {
	m := newTestModel("subj-1")
	m = update(t, m, questionFrame(t, 1, "Pick one"))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("enter on an options question with no selection should not submit")
	}
	if m.answered[1] {
		t.Fatal("an empty answer must not mark the question answered")
	}
	q, ok := m.focused()
	if !ok || q.ID != 1 {
		t.Fatalf("focus after ignored enter = %d (ok=%v), want still 1 (subject stays awaiting)", q.ID, ok)
	}

	// Selecting an option makes enter submit.
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter after selecting an option should submit")
	}
}

// TestEnterSubmitsOnNotesOnlyAnswer asserts a non-empty note alone unblocks
// submit even with no option selected: a deliberate free-text answer is valid. It
// exercises the REAL notes-only submit path — focus notes, type, then tab OUT so
// enter routes to submitCmd (not the textarea's cursor-blink) — and proves the
// submit actually fires by driving the submitted answer back through Update and
// asserting the question is marked answered. With notesFocused=true the enter
// would hit the textarea, so this would pass for the wrong reason.
func TestEnterSubmitsOnNotesOnlyAnswer(t *testing.T) {
	m := newTestModel("subj-1")
	m = update(t, m, questionFrame(t, 1, "Pick one"))

	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus notes
	m = update(t, m, keyRunes("none of these"))    // type a free-text note
	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab}) // tab OUT — enter now routes to submit
	if m.notesFocused {
		t.Fatal("notes must be unfocused so enter exercises the submit path, not the textarea")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter with a non-empty note should submit even with no option selected")
	}
	m = next.(Model)

	// Prove it was the OpAnswer-bearing submit and not a no-op: the built payload
	// carries the typed note, and feeding the submit outcome back marks it answered.
	a, ok := m.submit()
	if !ok || a.QuestionID != 1 || a.Notes != "none of these" || len(a.Selected) != 0 {
		t.Fatalf("submit payload = %+v (ok=%v), want question 1, note 'none of these', no options", a, ok)
	}
	m = update(t, m, submittedMsg{QuestionID: 1})
	if !m.answered[1] {
		t.Fatal("the notes-only submit must mark the question answered — canSubmit's notes branch is load-bearing")
	}
}

// TestEnterSubmitsOnNoOptionsQuestion asserts a free-text-only question (no
// options) submits on a bare enter — empty is legitimate when there's nothing to
// pick.
func TestEnterSubmitsOnNoOptionsQuestion(t *testing.T) {
	m := newTestModel("subj-1")
	q := interaction.QuestionPayload{Prompt: "Anything to add?"}
	m = update(t, m, liveEvent{Seq: 1, Type: interaction.EventQuestion, Data: frame(t, interaction.EventQuestion, q)})

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter on a no-options question should submit unconditionally")
	}
}

// TestReseedAddsOpenQuestion asserts the OpPending reseed surfaces a still-open
// question the resumed stream cursor skipped: the model shows the question as
// answerable, not "all questions answered". Before the reseed wiring this FAILS —
// the model only seeds from the stream, so a cursor past the seq leaves it empty.
func TestReseedAddsOpenQuestion(t *testing.T) {
	m := newTestModel("subj-1")
	// Simulate a relaunch: the stream resumed past seq 5 and delivered nothing,
	// so the model has no questions and would render "all questions answered".
	if _, ok := m.focused(); ok {
		t.Fatal("precondition: a fresh model has no focused question")
	}

	q := interaction.QuestionPayload{Prompt: "still open", Options: []interaction.Option{{Label: "A"}}}
	m = update(t, m, reseededMsg{Questions: []question{{ID: 5, Payload: q}}})

	focused, ok := m.focused()
	if !ok || focused.ID != 5 {
		t.Fatalf("reseed did not surface the open question: focus = %d (ok=%v), want 5", focused.ID, ok)
	}
	if m.openCount() != 1 {
		t.Fatalf("openCount = %d, want 1 after reseed", m.openCount())
	}
	if strings.Contains(m.render(), "all questions answered") {
		t.Fatal("reseed should keep the still-open question visible, not show all-answered")
	}
}

// TestReseedDeduplicatesAgainstStream asserts the reseed and the live stream
// never double-count a question: a question already delivered by the stream is
// not re-appended when the reseed carries the same id.
func TestReseedDeduplicatesAgainstStream(t *testing.T) {
	m := newTestModel("subj-1")
	m = update(t, m, questionFrame(t, 5, "from stream"))
	if len(m.questions) != 1 {
		t.Fatalf("precondition: questions = %d, want 1", len(m.questions))
	}

	q := interaction.QuestionPayload{Prompt: "from reseed", Options: []interaction.Option{{Label: "A"}}}
	m = update(t, m, reseededMsg{Questions: []question{{ID: 5, Payload: q}}})

	if len(m.questions) != 1 {
		t.Fatalf("reseed duplicated a stream question: %d questions, want 1", len(m.questions))
	}
	// The stream's copy wins: it arrived first and carries the live payload.
	if m.questions[0].Payload.Prompt != "from stream" {
		t.Fatalf("dedup kept the wrong copy: prompt = %q, want %q", m.questions[0].Payload.Prompt, "from stream")
	}
}

// TestReseedAnsweredQuestionStaysAnswered asserts a reseed re-delivering a
// question the human already answered (the projection lags the local submit) does
// not reopen it: it's deduped, and even if appended it stays answered.
func TestReseedDoesNotReopenAnswered(t *testing.T) {
	m := newTestModel("subj-1")
	m = update(t, m, questionFrame(t, 5, "answer me"))
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = update(t, m, submittedMsg{QuestionID: 5})
	if !m.answered[5] {
		t.Fatal("precondition: question 5 should be answered")
	}

	q := interaction.QuestionPayload{Prompt: "answer me", Options: []interaction.Option{{Label: "A"}}}
	m = update(t, m, reseededMsg{Questions: []question{{ID: 5, Payload: q}}})

	if m.openCount() != 0 {
		t.Fatalf("openCount = %d, want 0: reseed must not reopen an answered question", m.openCount())
	}
	if _, ok := m.focused(); ok {
		t.Fatal("an answered question reseeded should not steal focus")
	}
}
