package tui

import (
	"context"
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
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

// resolveIdleMsg reports a resolve poll that produced nothing to apply — the
// transient daemon-unavailable window during a version-skew swap — so the next
// tick may dispatch a fresh poll.
type resolveIdleMsg struct{}

// multiAwaitingMsg fires when a poll finds more than one awaiting subject in the
// scope — a transient, non-latching state. It carries an informative note the
// View shows while waiting; the next poll that settles to one (or none) awaiting
// subject clears it, so it never wedges the TUI.
type multiAwaitingMsg struct{}

// clearWaitNoteMsg fires when a poll finds the scope settled to zero awaiting
// subjects, clearing any prior multi-awaiting hint so the generic waiting line
// shows again.
type clearWaitNoteMsg struct{}

// multiAwaitingNote tells the human how to answer when the scope has more than
// one awaiting subject, which pickAwaiting cannot disambiguate on its own.
const multiAwaitingNote = "multiple awaiting subjects in this scope — answer via `cc-runtime answer` or close other windows"

// submittedMsg reports the outcome of an answer round-trip for the given
// question; Err is non-nil when the submit failed. Idled reports whether the
// answer released the subject's gate — set only on the remote path, where
// AnswerRemote returns it, so the View can show the remote release.
type submittedMsg struct {
	QuestionID int64
	Idled      bool
	Err        error
}

// meshResolvedMsg carries one mesh-wide resolve poll: the merged per-machine
// roster ListAll returned plus the single-awaiting decision pickMeshAwaiting made
// over it. It is the mesh analog of the local resolve path's resolved /
// multiAwaiting / clearWaitNote messages, folded into one so the roster and the
// decision arrive together.
type meshResolvedMsg struct {
	roster   []mesh.HostSubjects
	res      resolution
	found    bool
	multiple bool
}

// reseededMsg carries the subject's still-open questions read from the daemon's
// OpPending projection at resolve time, so a relaunch re-fetches every open
// question regardless of where the resumed stream cursor landed. Err is set only
// on the remote path, where pending is the sole question source: the model
// surfaces it and retries. A failed local fetch stays dropped — the live stream
// still delivers events.
type reseededMsg struct {
	Questions []question
	Err       error
}

// reseedRetryMsg re-runs the remote reseed after a failed fetch.
type reseedRetryMsg struct{}

// remoteCallTimeout bounds every mesh call a TUI command dispatches (resolve
// fan-out, reseed, answer): ssh's keepalives only catch dead connections, so a
// wedged remote `cc-runtime rpc` would otherwise hang its command forever.
const remoteCallTimeout = 30 * time.Second

// maxReseedFails caps the remote reseed retry loop: past it the failure latches
// into the fatal error screen instead of silently polling a broken peer forever.
const maxReseedFails = 5

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

	// reg, local, and dial are the mesh wiring: non-nil only when the registry has
	// peers. When nil the model runs the untouched local-only path (OpList over the
	// socket). When set, the resolve poll fans interaction.list across the mesh via
	// ListAll and a remote-resolved subject is answered over ssh via AnswerRemote.
	reg   *hostregistry.Registry
	local mesh.Runner
	dial  func(target string) mesh.Runner

	// roster is the last merged per-machine ListAll result, shown as the mesh
	// awaiting list while unresolved. Empty on the local-only path.
	roster []mesh.HostSubjects

	res      resolution
	resolved bool
	// resolving marks an in-flight resolve poll, so a tick never stacks a second
	// fan-out behind a slow one.
	resolving bool
	// idled records whether the last remote answer released the subject's gate, so
	// the done view can show the remote release.
	idled bool

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

	// waitNote is a non-latching informational line shown while unresolved (e.g.
	// the multiple-awaiting hint). Unlike err it never blocks the UI and clears on
	// the next poll that settles, so a transient scope-in-flux can't wedge the TUI.
	waitNote string

	// reseedNote surfaces a failed remote question fetch while the retry loop
	// runs, so a transient peer error never silently hides remote questions.
	reseedNote string
	// reseedFails counts consecutive failed remote reseeds toward maxReseedFails.
	reseedFails int

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

func reseedRetryTick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return reseedRetryMsg{} })
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
		if m.resolving {
			return m, pollTick()
		}
		m.resolving = true
		return m, tea.Batch(m.resolveCmd(), pollTick())

	case resolveIdleMsg:
		m.resolving = false
		return m, nil

	case multiAwaitingMsg:
		// Non-latching: surface the hint while still polling. The next poll that
		// settles to one awaiting subject resolves and clears it.
		m.resolving = false
		m.waitNote = multiAwaitingNote
		return m, nil

	case clearWaitNoteMsg:
		m.resolving = false
		m.waitNote = ""
		return m, nil

	case meshResolvedMsg:
		// A poll that lands after resolution is stale unless it re-resolves the
		// same subject: acting on it could swap the subject mid-answer.
		m.resolving = false
		if m.resolved && (!msg.found || msg.res.SubjectID != m.res.SubjectID) {
			return m, nil
		}
		// Store the roster, then reuse the local resolved/multi/clear handlers.
		m.roster = msg.roster
		switch {
		case msg.found:
			return m.Update(resolvedMsg{Res: msg.res})
		case msg.multiple:
			return m.Update(multiAwaitingMsg{})
		default:
			return m.Update(clearWaitNoteMsg{})
		}

	case resolvedMsg:
		// Stale unless unresolved or re-resolving the same subject (the stream
		// error recovery path).
		m.resolving = false
		if m.resolved && msg.Res.SubjectID != m.res.SubjectID {
			return m, nil
		}
		alreadyStreaming := m.resolved
		m.err = nil
		m.waitNote = ""
		m.resolved = true
		m.res = msg.Res
		// Start the stream once: a same-subject re-resolve must not attach a
		// second consumer to (and later double-close) the events channel.
		if !alreadyStreaming && m.startStream != nil {
			m.startStream(msg.Res)
		}
		return m, m.reseedCmd()

	case reseededMsg:
		if msg.Err != nil {
			m.reseedFails++
			if m.reseedFails >= maxReseedFails {
				m.reseedNote = ""
				m.err = msg.Err
				return m, nil
			}
			m.reseedNote = "fetching remote questions failed (" + msg.Err.Error() + "); retrying"
			return m, reseedRetryTick()
		}
		m.reseedFails = 0
		m.reseedNote = ""
		for _, q := range msg.Questions {
			if m.hasQuestion(q.ID) {
				continue
			}
			m.questions = append(m.questions, q)
		}
		m.advance()
		return m, nil

	case reseedRetryMsg:
		if !m.resolved {
			return m, nil
		}
		return m, m.reseedCmd()

	case liveEvent:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		m.ingest(msg)
		return m, m.readEvent()

	case submittedMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		m.idled = msg.Idled
		m.answered[msg.QuestionID] = true
		m.advance()
		return m, nil

	case errMsg:
		m.resolving = false
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
		if !m.canSubmit(q) {
			return m, nil
		}
		return m, m.submitCmd()
	}
	return m, nil
}

// canSubmit reports whether the focused question's current answer is non-empty.
// A question with options requires a selection or a note before enter releases
// the gate, so an accidental enter can't idle the subject with an empty answer.
// A question with no options is free-text-only and submits unconditionally.
func (m Model) canSubmit(q question) bool {
	if len(q.Payload.Options) == 0 {
		return true
	}
	if len(m.selected[q.ID]) > 0 {
		return true
	}
	return m.notes.Value() != ""
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

// meshEnabled reports whether the registry has peers, gating every mesh path. It
// is false on the local-only path, which then runs unchanged.
func (m Model) meshEnabled() bool { return m.reg != nil }

// remoteResolved reports whether the resolved subject lives on a peer, so the
// answer, reseed, and stream paths route over ssh instead of the local socket.
func (m Model) remoteResolved() bool { return m.meshEnabled() && !m.res.Local }

// machineLabel is the resolved subject's machine label for display.
func (m Model) machineLabel() string {
	if m.res.Local {
		return "local"
	}
	return hostregistry.HostNode(m.res.Host)
}

// submitCmd builds the payload and submits it, reporting the outcome back into
// Update as a submittedMsg. A local subject answers over the socket; a remote one
// routes through AnswerRemote over ssh, carrying back whether the answer released
// the remote gate.
func (m Model) submitCmd() tea.Cmd {
	a, ok := m.submit()
	if !ok {
		return nil
	}
	if m.remoteResolved() {
		host := m.res.Host
		runner := m.dial(host)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), remoteCallTimeout)
			defer cancel()
			idled, err := mesh.AnswerRemote(ctx, runner, host, a)
			if err != nil {
				return submittedMsg{QuestionID: a.QuestionID, Err: err}
			}
			return submittedMsg{QuestionID: a.QuestionID, Idled: idled}
		}
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

// resolveCmd runs one OpList round-trip; on a single awaiting subject it emits a
// resolvedMsg. More than one awaiting subject emits a non-latching
// multiAwaitingMsg (keep polling, but show the hint); a settled scope with zero
// awaiting emits clearWaitNoteMsg so a prior multi-awaiting hint clears. A
// daemon-unavailable error is transient — the documented version-skew swap leaves
// the socket briefly unreachable — so it is swallowed to keep polling rather than
// latched into a fatal error screen.
func (m Model) resolveCmd() tea.Cmd {
	if m.meshEnabled() {
		return m.meshResolveCmd()
	}
	scope := m.scope
	client := m.client
	return func() tea.Msg {
		res, found, multiple, err := resolveAwaiting(context.Background(), client, scope)
		if errors.Is(err, daemon.ErrDaemonUnavailable) {
			return resolveIdleMsg{}
		}
		if err != nil {
			return errMsg{Err: err}
		}
		if found {
			return resolvedMsg{Res: res}
		}
		if multiple {
			return multiAwaitingMsg{}
		}
		return clearWaitNoteMsg{}
	}
}

// meshResolveCmd runs one mesh-wide resolve poll: ListAll fans interaction.list
// across every machine, and pickMeshAwaiting resolves the single awaiting subject
// over the merged roster. A malformed reply from a live peer fails loud into an
// errMsg; an unreachable peer is folded into the roster and never latches an
// error, matching the local path's tolerance of a transient daemon.
func (m Model) meshResolveCmd() tea.Cmd {
	reg := m.reg
	scope := m.scope
	local := m.local
	dial := m.dial
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteCallTimeout)
		defer cancel()
		results, err := mesh.ListAll(ctx, reg, scope, local, dial)
		if err != nil {
			return errMsg{Err: err}
		}
		res, found, multiple := pickMeshAwaiting(results)
		return meshResolvedMsg{roster: results, res: res, found: found, multiple: multiple}
	}
}

// reseedCmd reads the resolved subject's still-open questions from the daemon's
// OpPending projection and folds them in as a reseededMsg. It runs at resolve
// time so a relaunch — where the resumed stream cursor has already advanced past
// the open question's seq and delivers nothing — still re-surfaces every open
// question. A fetch error is non-fatal: the live stream remains the primary
// source, so a failed reseed yields no questions rather than a fatal error.
func (m Model) reseedCmd() tea.Cmd {
	if m.remoteResolved() {
		return m.remoteReseedCmd()
	}
	scope := m.scope
	client := m.client
	subjectID := m.res.SubjectID
	return func() tea.Msg {
		qs, err := fetchPending(context.Background(), client, scope, subjectID)
		if err != nil {
			return reseededMsg{}
		}
		return reseededMsg{Questions: qs}
	}
}

// remoteReseedCmd reads a remote subject's open questions via PendingAll — the
// remote path has no SSE stream, so pending is its only question source. Any
// failure, including the owning host's per-host error, comes back as
// reseededMsg.Err so the model surfaces it and retries.
func (m Model) remoteReseedCmd() tea.Cmd {
	reg := m.reg
	local := m.local
	dial := m.dial
	subjectID := m.res.SubjectID
	host := m.res.Host
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteCallTimeout)
		defer cancel()
		results, err := mesh.PendingAll(ctx, reg, subjectID, local, dial)
		if err != nil {
			return reseededMsg{Err: err}
		}
		for _, h := range results {
			if h.Host == host && h.Err != nil {
				return reseededMsg{Err: h.Err}
			}
		}
		qs, err := remoteQuestions(results)
		if err != nil {
			return reseededMsg{Err: err}
		}
		return reseededMsg{Questions: qs}
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
