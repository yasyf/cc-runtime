package interaction

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
)

type startReply struct {
	SubjectID string `json:"subject_id"`
}

type askReply struct {
	SubjectID  string `json:"subject_id"`
	QuestionID int64  `json:"question_id"`
}

type pollBody struct {
	SubjectID  string `json:"subject_id"`
	QuestionID int64  `json:"question_id"`
}

type pollReply struct {
	Answered bool            `json:"answered"`
	Answer   json.RawMessage `json:"answer,omitempty"`
}

type answerReply struct {
	Idled bool `json:"idled"`
}

type subjectBody struct {
	SubjectID string `json:"subject_id"`
}

type pendingReply struct {
	Questions []PendingQuestion `json:"questions"`
}

type listBody struct {
	Scope string `json:"scope"`
}

type listReply struct {
	Subjects []ListedSubject `json:"subjects"`
	HTTPPort int             `json:"http_port"`
}

// Fanout carries every durably appended question and notification beyond the
// SSE plane (Web Push). Handlers invoke it inline after Append succeeds, so
// implementations return immediately and deliver in the background.
type Fanout interface {
	Question(subjectID string, q QuestionPayload)
	Notification(subjectID string, n NotificationPayload)
}

// handlers binds the append-observing ops to the fan-out they feed.
type handlers struct {
	fanout Fanout
}

// Register wires every interaction op onto the daemon.
func Register(s *daemon.Server, fanout Fanout) {
	h := handlers{fanout: fanout}
	s.Register(OpStart, handleStart)
	s.Register(OpAsk, h.handleAsk)
	s.Register(OpNotify, h.handleNotify)
	s.Register(OpAnswer, handleAnswer)
	s.Register(OpAnswerPoll, handleAnswerPoll)
	s.Register(OpPending, handlePending)
	s.Register(OpList, handleList)
	s.Register(OpCaptureNotification, h.handleCaptureNotification)
	s.Register(OpCaptureQuestion, h.handleCaptureQuestion)
}

func handleStart(hc daemon.HandlerCtx) daemon.Reply {
	sub, _, err := hc.Subjects.Start(hc.Ctx, hc.Window, hc.Scope, slugFor(hc.Scope, hc.Window.Session), Lifecycle, false)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(startReply{SubjectID: sub.ID})
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: body}
}

// handleAsk publishes the question, then atomically projects the open row AND
// engages the gate (insertPendingAndAwait, one tx). There is no transactional
// append spanning the event log and the projection, and the question's seq does
// not exist until Append returns, so the gate cannot be engaged before publish;
// the design instead keeps every post-publish failure fail-closed.
//
// Ordering: Start (resume/create — DO NOT pre-flip status) → Append → atomic
// insert-row+await. On Append failure there is NO rollback: this attempt never
// touched the status, so it stays consistent with the real open-question count
// (awaiting if a prior question is still open on a resumed subject, idle if the
// subject was fresh). On insert+await failure the question is already durable, so
// the fail-CLOSED fallback re-engages awaiting — the published question must
// never leave the gate idle.
//
// Concurrency (the gate invariant): handleAsk resumes an EXISTING subject, so a
// 2nd ask can run while a prior question q1 is still open and the subject is
// awaiting; a concurrent handleAnswer answering q1 runs recordAnswer (one tx).
// insert-row+await is also one tx, and SetMaxOpenConns(1) lets the two txs
// interleave only at a tx boundary, never mid-tx. Trace both orderings of the
// 2nd ask's atomic tx (A) vs the concurrent recordAnswer (R) on q1:
//   - R before A: R idles the subject (q1 was the last open row), THEN A inserts
//     the new row and sets awaiting in the same tx → ends awaiting, new row open.
//   - A before R: A inserts the new row and sets awaiting, THEN R answers q1, its
//     open-count still sees the new row (open>0) → does not idle → stays awaiting.
//
// Either interleaving ends at status=awaiting with the open question present. The
// pre-flip+separate-insert split this replaces had a window where status=awaiting
// but the new row was absent, so an interleaved recordAnswer could idle the
// subject and then the insert would land an open row under an idle (released) gate.
func (h handlers) handleAsk(hc daemon.HandlerCtx) daemon.Reply {
	var q QuestionPayload
	if err := json.Unmarshal(hc.Env.Body, &q); err != nil {
		return daemon.Reply{OK: false, Error: "bad question body: " + err.Error()}
	}
	sub, _, err := hc.Subjects.Start(hc.Ctx, hc.Window, hc.Scope, slugFor(hc.Scope, hc.Window.Session), Lifecycle, false)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}

	payload, _ := json.Marshal(q)
	seq, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: sub.ID, Origin: event.OriginAgent, Type: EventQuestion, Payload: wireEvent(EventQuestion, q),
	})
	if err != nil {
		// No question was published and this attempt never touched the status, so
		// it remains consistent with the actual open-question count — nothing to
		// roll back. (A resumed subject with a prior open question stays awaiting; a
		// fresh subject stays idle.)
		return daemon.Reply{OK: false, Error: err.Error()}
	}

	// The question is durable. Atomically project the open row and engage the gate
	// on a daemon-owned context so a client cancellation here cannot strand the
	// projection. On failure the question is already published, so re-engage
	// awaiting as the fail-CLOSED fallback — never leave the gate idle behind a
	// visible question. Accepted edge: a non-busy insert that fails leaves the
	// subject awaiting with no pending row (a wedge the gate keeps closed); this is
	// the intended fail-closed tradeoff, recoverable when the agent re-asks.
	ctx, cancel := cleanupCtx()
	defer cancel()
	if err := withBusyRetry(ctx, func() error {
		return insertPendingAndAwait(ctx, hc.DB, sub.ID, seq, q.Header, string(payload))
	}); err != nil {
		_ = setSubjectStatus(ctx, hc.DB, sub.ID, StatusAwaiting)
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	h.fanout.Question(sub.ID, q)
	body, _ := json.Marshal(askReply{SubjectID: sub.ID, QuestionID: seq})
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: body}
}

func (h handlers) handleNotify(hc daemon.HandlerCtx) daemon.Reply {
	var n NotificationPayload
	if err := json.Unmarshal(hc.Env.Body, &n); err != nil {
		return daemon.Reply{OK: false, Error: "bad notification body: " + err.Error()}
	}
	sub, _, err := hc.Subjects.Start(hc.Ctx, hc.Window, hc.Scope, slugFor(hc.Scope, hc.Window.Session), Lifecycle, false)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if _, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: sub.ID, Origin: event.OriginAgent, Type: EventNotification, Payload: wireEvent(EventNotification, n),
	}); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	h.fanout.Notification(sub.ID, n)
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: json.RawMessage(`{"ok":true}`)}
}

// handleAnswer logs the answer before projecting it (append-first). The gate is
// subjects.status, never the answer event's own seq, so nothing keys off that
// seq and append-first is safe. Ordering: pre-validate → Append → project, so
// the gate releases only on an answer that is durably in the log, and a retry of
// a partially-applied answer re-Appends (a deduped no-op) and re-runs the
// projection to completion.
func handleAnswer(hc daemon.HandlerCtx) daemon.Reply {
	var a AnswerPayload
	if err := json.Unmarshal(hc.Env.Body, &a); err != nil {
		return daemon.Reply{OK: false, Error: "bad answer body: " + err.Error()}
	}

	known, answered, err := questionState(hc.Ctx, hc.DB, a.SubjectID, a.QuestionID)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if !known {
		return daemon.Reply{OK: false, Error: fmt.Sprintf("answer targets unknown question (subject=%s id=%d)", a.SubjectID, a.QuestionID)}
	}
	if answered {
		// Idempotent: the question is already answered. Do not append, do not
		// overwrite. Report idled from the current open-question count.
		open, err := openCount(hc.Ctx, hc.DB, a.SubjectID)
		if err != nil {
			return daemon.Reply{OK: false, Error: err.Error()}
		}
		body, _ := json.Marshal(answerReply{Idled: open == 0})
		return daemon.Reply{OK: true, SubjectID: a.SubjectID, Body: body}
	}

	payload, _ := json.Marshal(a)
	if _, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: a.SubjectID, Origin: event.OriginHuman, Type: EventAnswer,
		Payload:  wireEvent(EventAnswer, a),
		DedupKey: "answer:" + a.SubjectID + ":" + strconv.FormatInt(a.QuestionID, 10),
	}); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}

	// The answer is durable. Project it on a daemon-owned cleanup context (derived
	// from context.Background, not hc.Ctx) so the projection completes even as
	// hc.Ctx is being cancelled by daemon shutdown. If this fails the status stays
	// awaiting (fail-closed) and pollAnswer still reports not-answered, so the agent
	// keeps waiting; a retry re-Appends (deduped) and re-projects.
	ctx, cancel := cleanupCtx()
	defer cancel()
	var idled bool
	if err := withBusyRetry(ctx, func() error {
		var err error
		idled, err = recordAnswer(ctx, hc.DB, a.SubjectID, a.QuestionID, string(payload))
		return err
	}); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(answerReply{Idled: idled})
	return daemon.Reply{OK: true, SubjectID: a.SubjectID, Body: body}
}

func handleAnswerPoll(hc daemon.HandlerCtx) daemon.Reply {
	var b pollBody
	if err := json.Unmarshal(hc.Env.Body, &b); err != nil {
		return daemon.Reply{OK: false, Error: "bad poll body: " + err.Error()}
	}
	answered, answer, err := pollAnswer(hc.Ctx, hc.DB, b.SubjectID, b.QuestionID)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	reply := pollReply{Answered: answered}
	if answered {
		reply.Answer = json.RawMessage(answer)
	}
	body, _ := json.Marshal(reply)
	return daemon.Reply{OK: true, Body: body}
}

func handlePending(hc daemon.HandlerCtx) daemon.Reply {
	var b subjectBody
	if err := json.Unmarshal(hc.Env.Body, &b); err != nil {
		return daemon.Reply{OK: false, Error: "bad pending body: " + err.Error()}
	}
	questions, err := openQuestions(hc.Ctx, hc.DB, b.SubjectID)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(pendingReply{Questions: questions})
	return daemon.Reply{OK: true, Body: body}
}

func handleList(hc daemon.HandlerCtx) daemon.Reply {
	scope := hc.Scope
	if len(hc.Env.Body) > 0 {
		var b listBody
		if err := json.Unmarshal(hc.Env.Body, &b); err != nil {
			return daemon.Reply{OK: false, Error: "bad list body: " + err.Error()}
		}
		if b.Scope != "" {
			scope = b.Scope
		}
	}
	subjects, err := listSubjects(hc.Ctx, hc.DB, scope)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(listReply{Subjects: subjects, HTTPPort: hc.HTTPPort})
	return daemon.Reply{OK: true, HTTPPort: hc.HTTPPort, Body: body}
}

func (h handlers) handleCaptureNotification(hc daemon.HandlerCtx) daemon.Reply {
	var n NotificationPayload
	if err := json.Unmarshal(hc.Env.Body, &n); err != nil {
		return daemon.Reply{OK: false, Error: "bad notification body: " + err.Error()}
	}
	sub, ok, err := hc.Subjects.Find(hc.Ctx, hc.Window, hc.Scope)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if !ok {
		return daemon.Reply{OK: true, Body: json.RawMessage(`{"ok":true}`)}
	}
	if _, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: sub.ID, Origin: event.OriginSystem, Type: EventNotification, Payload: wireEvent(EventNotification, n),
	}); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	h.fanout.Notification(sub.ID, n)
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: json.RawMessage(`{"ok":true}`)}
}

// handleCaptureQuestion mirrors a native AskUserQuestion the agent emitted on the
// soft-steer path — where no cc-runtime ask was used, so the question would
// otherwise render only in the terminal and vanish — into the subject log as a
// system-origin event. It is a one-way MIRROR for durability and remote
// visibility, not an open cc-runtime question: the human answers the native
// picker in the terminal, where the agent is already blocked. So it appends an
// EventNotification (never EventQuestion): it does NOT flip the subject to
// awaiting and does NOT project a pending_questions row, so it cannot engage the
// edit gate or wedge the subject on a question no cc-runtime answer could resolve.
// As a notification frame it lands in the TUI feed, never the answerable queue,
// and never inflates the open-question count. Shape mirrors
// handleCaptureNotification: Find subject → Append, OriginSystem, no status
// mutation.
func (h handlers) handleCaptureQuestion(hc daemon.HandlerCtx) daemon.Reply {
	var q QuestionPayload
	if err := json.Unmarshal(hc.Env.Body, &q); err != nil {
		return daemon.Reply{OK: false, Error: "bad question body: " + err.Error()}
	}
	sub, ok, err := hc.Subjects.Find(hc.Ctx, hc.Window, hc.Scope)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if !ok {
		return daemon.Reply{OK: true, Body: json.RawMessage(`{"ok":true}`)}
	}
	n := NotificationPayload{Message: captureQuestionMessage(q)}
	if _, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: sub.ID, Origin: event.OriginSystem, Type: EventNotification, Payload: wireEvent(EventNotification, n),
	}); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	h.fanout.Notification(sub.ID, n)
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: json.RawMessage(`{"ok":true}`)}
}

// captureQuestionMessage renders a captured native AskUserQuestion into a feed
// line: the header chip prefixes the prompt when both are present.
func captureQuestionMessage(q QuestionPayload) string {
	switch {
	case q.Header != "" && q.Prompt != "":
		return q.Header + ": " + q.Prompt
	case q.Prompt != "":
		return q.Prompt
	default:
		return q.Header
	}
}
