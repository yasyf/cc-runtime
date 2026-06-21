package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

// tuiConsumer is the stream-consumer name the TUI registers under, keeping its
// own resume cursor distinct from the agent's watch consumer.
const tuiConsumer = "tui"

// listReply mirrors the OpList daemon reply: the scope's active subjects and the
// HTTP port the events plane is on.
type listReply struct {
	Subjects []interaction.ListedSubject `json:"subjects"`
	HTTPPort int                         `json:"http_port"`
}

// resolution is the awaiting subject the TUI attaches to: its id and the events
// plane port the consumer streams from.
type resolution struct {
	SubjectID string
	HTTPPort  int
}

// resolveAwaiting asks the daemon for the scope's subjects and picks the single
// awaiting one. found is false when no subject is awaiting yet, so the caller can
// keep polling whether the agent asks before or after the human opens the TUI.
// multiple is true when more than one subject is awaiting — a transient
// scope-in-flux state the caller surfaces as an informative (non-latching) note
// rather than picking one arbitrarily.
func resolveAwaiting(ctx context.Context, client *daemon.Client, scope string) (res resolution, found, multiple bool, err error) {
	reqBody, _ := json.Marshal(map[string]string{"scope": scope})
	r, err := client.Do(ctx, daemon.Envelope{Op: interaction.OpList, Scope: scope, Body: reqBody})
	if err != nil {
		return resolution{}, false, false, err
	}
	if !r.OK {
		return resolution{}, false, false, errors.New(r.Error)
	}
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		return resolution{}, false, false, err
	}
	res, found, multiple = pickAwaiting(lr)
	return res, found, multiple, nil
}

// pendingReply mirrors the OpPending daemon reply: the subject's still-open
// questions projected from pending_questions, independent of any stream cursor.
type pendingReply struct {
	Questions []interaction.PendingQuestion `json:"questions"`
}

// fetchPending asks the daemon for the subject's still-open questions via
// OpPending — the durable projection the daemon owns, not the per-consumer stream
// cursor — so a relaunch reseeds every open question regardless of where the
// resumed stream's cursor landed.
func fetchPending(ctx context.Context, client *daemon.Client, scope, subjectID string) ([]question, error) {
	reqBody, _ := json.Marshal(map[string]string{"subject_id": subjectID})
	r, err := client.Do(ctx, daemon.Envelope{Op: interaction.OpPending, Scope: scope, Body: reqBody})
	if err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, errors.New(r.Error)
	}
	var pr pendingReply
	if err := json.Unmarshal(r.Body, &pr); err != nil {
		return nil, err
	}
	out := make([]question, 0, len(pr.Questions))
	for _, pq := range pr.Questions {
		q, err := parseQuestion(pq.QuestionID, pq.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, nil
}

// pickAwaiting selects the single awaiting subject from a list reply. It is split
// out so the resolution logic is testable without a live socket. More than one
// awaiting subject is a transient scope-in-flux state, not a fatal error: found
// is false and multiple is true so the caller keeps polling — surfacing an
// informative note while it waits — until the scope settles to one subject.
func pickAwaiting(lr listReply) (res resolution, found, multiple bool) {
	awaiting := ""
	for _, s := range lr.Subjects {
		if s.Status != interaction.StatusAwaiting {
			continue
		}
		if awaiting != "" {
			return resolution{}, false, true
		}
		awaiting = s.SubjectID
	}
	if awaiting == "" {
		return resolution{}, false, false
	}
	return resolution{SubjectID: awaiting, HTTPPort: lr.HTTPPort}, true, false
}

// listPort re-resolves the daemon's current HTTP events port via OpList,
// regardless of whether any subject is awaiting, so a stream consumer can refresh
// its handshake after the subject it's attached to idles.
func listPort(ctx context.Context, client *daemon.Client, scope string) (int, error) {
	reqBody, _ := json.Marshal(map[string]string{"scope": scope})
	r, err := client.Do(ctx, daemon.Envelope{Op: interaction.OpList, Scope: scope, Body: reqBody})
	if err != nil {
		return 0, err
	}
	if !r.OK {
		return 0, errors.New(r.Error)
	}
	return r.HTTPPort, nil
}

// submitAnswer sends the human's answer over the unix socket (never HTTP). The
// daemon appends the answer event and idles the subject when it was the last open
// question.
func submitAnswer(ctx context.Context, client *daemon.Client, scope string, a interaction.AnswerPayload) error {
	body, _ := json.Marshal(a)
	r, err := client.Do(ctx, daemon.Envelope{Op: interaction.OpAnswer, Scope: scope, Body: body})
	if err != nil {
		return err
	}
	if !r.OK {
		return errors.New(r.Error)
	}
	return nil
}

// eventType extracts the `type` discriminator the wire frame carries inside the
// payload (the SSE plane streams only the payload, not the event's Type column).
func eventType(data string) string {
	var e struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return ""
	}
	return e.Type
}

// parseQuestion decodes a streamed interaction.question frame into its question
// payload plus the seq that identifies it for an answer.
func parseQuestion(seq int64, data string) (question, error) {
	var q interaction.QuestionPayload
	if err := json.Unmarshal([]byte(data), &q); err != nil {
		return question{}, fmt.Errorf("decode question: %w", err)
	}
	return question{ID: seq, Payload: q}, nil
}

// parseNotification decodes a streamed interaction.notification frame.
func parseNotification(data string) (interaction.NotificationPayload, error) {
	var n interaction.NotificationPayload
	if err := json.Unmarshal([]byte(data), &n); err != nil {
		return interaction.NotificationPayload{}, fmt.Errorf("decode notification: %w", err)
	}
	return n, nil
}
