package tui

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-runtime/interaction"
)

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
	res, found, err := pickAwaiting(lr)
	if err != nil {
		t.Fatalf("pickAwaiting: %v", err)
	}
	if !found {
		t.Fatal("pickAwaiting did not find the awaiting subject")
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
	_, found, err := pickAwaiting(lr)
	if err != nil {
		t.Fatalf("pickAwaiting: %v", err)
	}
	if found {
		t.Fatal("pickAwaiting found a subject with none awaiting")
	}
}

func TestPickAwaitingRejectsMultiple(t *testing.T) {
	lr := listReply{
		Subjects: []interaction.ListedSubject{
			{SubjectID: "a", Status: interaction.StatusAwaiting},
			{SubjectID: "b", Status: interaction.StatusAwaiting},
		},
	}
	if _, _, err := pickAwaiting(lr); err == nil {
		t.Fatal("pickAwaiting should reject more than one awaiting subject")
	}
}
