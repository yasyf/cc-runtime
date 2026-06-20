package interaction

import (
	"encoding/json"

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

// Register wires every interaction op onto the daemon.
func Register(s *daemon.Server) {
	s.Register(OpStart, handleStart)
	s.Register(OpAsk, handleAsk)
	s.Register(OpNotify, handleNotify)
	s.Register(OpAnswer, handleAnswer)
	s.Register(OpAnswerPoll, handleAnswerPoll)
	s.Register(OpPending, handlePending)
	s.Register(OpList, handleList)
	s.Register(OpCaptureNotification, handleCaptureNotification)
}

func handleStart(hc daemon.HandlerCtx) daemon.Reply {
	sub, _, err := hc.Subjects.Start(hc.Ctx, hc.Window, hc.Scope, slugFor(hc.Scope, hc.Window.Session), Lifecycle, false)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(startReply{SubjectID: sub.ID})
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: body}
}

func handleAsk(hc daemon.HandlerCtx) daemon.Reply {
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
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if err := insertPendingAndAwait(hc.Ctx, hc.DB, sub.ID, seq, q.Header, string(payload)); err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(askReply{SubjectID: sub.ID, QuestionID: seq})
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: body}
}

func handleNotify(hc daemon.HandlerCtx) daemon.Reply {
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
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: json.RawMessage(`{"ok":true}`)}
}

func handleAnswer(hc daemon.HandlerCtx) daemon.Reply {
	var a AnswerPayload
	if err := json.Unmarshal(hc.Env.Body, &a); err != nil {
		return daemon.Reply{OK: false, Error: "bad answer body: " + err.Error()}
	}
	payload, _ := json.Marshal(a)
	idled, err := recordAnswer(hc.Ctx, hc.DB, a.SubjectID, a.QuestionID, string(payload))
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	if _, err := hc.Append(hc.Ctx, &event.Event{
		SubjectID: a.SubjectID, Origin: event.OriginHuman, Type: EventAnswer, Payload: wireEvent(EventAnswer, a),
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

func handleCaptureNotification(hc daemon.HandlerCtx) daemon.Reply {
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
	return daemon.Reply{OK: true, SubjectID: sub.ID, Body: json.RawMessage(`{"ok":true}`)}
}
