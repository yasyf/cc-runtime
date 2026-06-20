package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

// liveEvent is one frame off the subject's event stream, fed into the Update
// loop from the consumer goroutine over the events channel. A non-nil Err is the
// stream's terminal frame: the consumer died and no more events will arrive.
type liveEvent struct {
	Seq  int64
	Type string
	Data string
	Err  error
}

// resolvedMsg carries the awaiting subject the poll resolved. Until it arrives
// the model shows a waiting state and keeps ticking OpList.
type resolvedMsg struct{ Res resolution }

// pollTickMsg fires the OpList refresh while no subject is awaiting yet.
type pollTickMsg struct{}

// submittedMsg reports the outcome of an OpAnswer round-trip for the given
// question; Err is non-nil when the submit failed.
type submittedMsg struct {
	QuestionID int64
	Err        error
}

// errMsg surfaces a fatal resolution or stream error into the View.
type errMsg struct{ Err error }

// question is one open ask the human must answer, identified by the seq the
// daemon assigned the question event (its QuestionID for an answer).
type question struct {
	ID      int64
	Payload interaction.QuestionPayload
}

// notification is one feed entry: an agent or system message.
type notification struct {
	Message string
	Urgency string
}

// Model is the TUI's full state. It is a pure value: Update mutates and returns
// it, View renders it, and submit builds the answer payload — none of them touch
// the socket, so the whole core is testable without a daemon.
type Model struct {
	scope  string
	client *daemon.Client
	events <-chan liveEvent
	// startStream launches the SSE consumer for the resolved subject, feeding the
	// events channel. The command injects it so the model stays free of network
	// code; nil in tests, which drive the events channel directly.
	startStream func(resolution)

	res      resolution
	resolved bool

	questions []question
	focus     int

	// selected tracks chosen option labels per question id; the inner map is a
	// set keyed by label so a multi-select toggle is O(1) and order-free.
	selected map[int64]map[string]bool
	// cursor is the highlighted option row within the focused question.
	cursor map[int64]int
	// answered marks questions the human has submitted, so the focus advances
	// past them and the all-answered state shows once the queue drains.
	answered map[int64]bool

	notes textarea.Model
	// notesFocused routes key input to the notes field instead of the option list.
	notesFocused bool

	feed []notification

	width  int
	height int

	err  error
	quit bool
}

// NewModel builds the initial TUI state for a scope. client and events are the
// socket and live-stream boundaries; tests pass a nil client and a closed channel
// to exercise the pure core.
func NewModel(scope string, client *daemon.Client, events <-chan liveEvent) Model {
	ta := textarea.New()
	ta.Placeholder = "notes (optional)"
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	return Model{
		scope:    scope,
		client:   client,
		events:   events,
		selected: map[int64]map[string]bool{},
		cursor:   map[int64]int{},
		answered: map[int64]bool{},
		notes:    ta,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.readEvent(), pollTick())
}

// readEvent is the tea.Cmd that pulls one live event off the stream channel and
// turns it into a message, re-arming itself each time so the loop drains forever.
func (m Model) readEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return nil
		}
		return ev
	}
}

func pollTick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.notes.SetWidth(msg.Width - 4)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case pollTickMsg:
		if m.resolved {
			return m, nil
		}
		return m, tea.Batch(m.resolveCmd(), pollTick())

	case resolvedMsg:
		m.resolved = true
		m.res = msg.Res
		if m.startStream != nil {
			m.startStream(msg.Res)
		}
		return m, nil

	case liveEvent:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.ingest(msg)
		return m, m.readEvent()

	case submittedMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.answered[msg.QuestionID] = true
		m.advance()
		return m, nil

	case errMsg:
		m.err = msg.Err
		return m, nil
	}
	return m, nil
}

// handleKey routes key presses: global quit, notes-field editing, option cursor
// movement, multi/single-select toggling, and submit.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "tab":
		m.notesFocused = !m.notesFocused
		if m.notesFocused {
			return m, m.notes.Focus()
		}
		m.notes.Blur()
		return m, nil
	}

	if m.notesFocused {
		var cmd tea.Cmd
		m.notes, cmd = m.notes.Update(msg)
		return m, cmd
	}

	q, ok := m.focused()
	if !ok {
		if msg.String() == "q" {
			m.quit = true
			return m, tea.Quit
		}
		return m, nil
	}

	switch msg.String() {
	case "q":
		m.quit = true
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(q, -1)
	case "down", "j":
		m.moveCursor(q, 1)
	case " ", "x":
		m.toggle(q)
	case "enter":
		return m, m.submitCmd()
	}
	return m, nil
}

// focused returns the question the human is currently answering, if any.
func (m Model) focused() (question, bool) {
	if m.focus < 0 || m.focus >= len(m.questions) {
		return question{}, false
	}
	return m.questions[m.focus], true
}

// moveCursor walks the option cursor within bounds.
func (m *Model) moveCursor(q question, delta int) {
	n := len(q.Payload.Options)
	if n == 0 {
		return
	}
	c := m.cursor[q.ID] + delta
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	m.cursor[q.ID] = c
}

// toggle flips the option under the cursor. A single-select question clears its
// other choices first so exactly one stays selected; multi-select accumulates.
func (m *Model) toggle(q question) {
	opts := q.Payload.Options
	c := m.cursor[q.ID]
	if c < 0 || c >= len(opts) {
		return
	}
	label := opts[c].Label
	set := m.selected[q.ID]
	if set == nil {
		set = map[string]bool{}
		m.selected[q.ID] = set
	}
	if !q.Payload.MultiSelect {
		on := set[label]
		clear(set)
		if !on {
			set[label] = true
		}
		return
	}
	if set[label] {
		delete(set, label)
	} else {
		set[label] = true
	}
}

// submit builds the answer payload for the focused question from the current
// selection and notes. It is pure: the test drives selection + notes through the
// real Update path, then asserts on this exact payload.
func (m Model) submit() (interaction.AnswerPayload, bool) {
	q, ok := m.focused()
	if !ok {
		return interaction.AnswerPayload{}, false
	}
	selected := []string{}
	for _, opt := range q.Payload.Options {
		if m.selected[q.ID][opt.Label] {
			selected = append(selected, opt.Label)
		}
	}
	return interaction.AnswerPayload{
		SubjectID:  m.res.SubjectID,
		QuestionID: q.ID,
		Selected:   selected,
		Notes:      m.notes.Value(),
	}, true
}

// submitCmd builds the payload and submits it over the socket, reporting the
// outcome back into Update as a submittedMsg.
func (m Model) submitCmd() tea.Cmd {
	a, ok := m.submit()
	if !ok {
		return nil
	}
	scope := m.scope
	client := m.client
	return func() tea.Msg {
		if err := submitAnswer(context.Background(), client, scope, a); err != nil {
			return submittedMsg{QuestionID: a.QuestionID, Err: err}
		}
		return submittedMsg{QuestionID: a.QuestionID}
	}
}

// resolveCmd runs one OpList round-trip; on an awaiting subject it emits a
// resolvedMsg, otherwise nothing (the next tick retries).
func (m Model) resolveCmd() tea.Cmd {
	scope := m.scope
	client := m.client
	return func() tea.Msg {
		res, found, err := resolveAwaiting(context.Background(), client, scope)
		if err != nil {
			return errMsg{Err: err}
		}
		if !found {
			return nil
		}
		return resolvedMsg{Res: res}
	}
}

// ingest folds one live event into the model: a question joins the queue, a
// notification joins the feed. The answer echo is ignored — the local submit
// already advanced the focus.
func (m *Model) ingest(ev liveEvent) {
	switch ev.Type {
	case interaction.EventQuestion:
		q, err := parseQuestion(ev.Seq, ev.Data)
		if err != nil {
			m.err = err
			return
		}
		if m.hasQuestion(q.ID) {
			return
		}
		m.questions = append(m.questions, q)
		m.advance()
	case interaction.EventNotification:
		n, err := parseNotification(ev.Data)
		if err != nil {
			m.err = err
			return
		}
		m.feed = append(m.feed, notification{Message: n.Message, Urgency: n.Urgency})
	}
}

func (m Model) hasQuestion(id int64) bool {
	for _, q := range m.questions {
		if q.ID == id {
			return true
		}
	}
	return false
}

// advance moves the focus to the first unanswered question, resetting the notes
// field so each question starts clean.
func (m *Model) advance() {
	for i, q := range m.questions {
		if !m.answered[q.ID] {
			if i != m.focus {
				m.notes.Reset()
				m.notes.Blur()
				m.notesFocused = false
			}
			m.focus = i
			return
		}
	}
	m.focus = len(m.questions)
}

// openCount reports how many questions remain unanswered, for the header.
func (m Model) openCount() int {
	n := 0
	for _, q := range m.questions {
		if !m.answered[q.ID] {
			n++
		}
	}
	return n
}

func (m Model) View() string {
	return m.render()
}
