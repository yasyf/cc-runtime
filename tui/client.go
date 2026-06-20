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
func resolveAwaiting(ctx context.Context, client *daemon.Client, scope string) (res resolution, found bool, err error) {
	reqBody, _ := json.Marshal(map[string]string{"scope": scope})
	r, err := client.Do(ctx, daemon.Envelope{Op: interaction.OpList, Scope: scope, Body: reqBody})
	if err != nil {
		return resolution{}, false, err
	}
	if !r.OK {
		return resolution{}, false, errors.New(r.Error)
	}
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		return resolution{}, false, err
	}
	return pickAwaiting(lr)
}

// pickAwaiting selects the single awaiting subject from a list reply. It is split
// out so the resolution logic is testable without a live socket.
func pickAwaiting(lr listReply) (resolution, bool, error) {
	awaiting := ""
	for _, s := range lr.Subjects {
		if s.Status != interaction.StatusAwaiting {
			continue
		}
		if awaiting != "" {
			return resolution{}, false, errors.New("multiple awaiting subjects in scope; the TUI answers a single subject")
		}
		awaiting = s.SubjectID
	}
	if awaiting == "" {
		return resolution{}, false, nil
	}
	return resolution{SubjectID: awaiting, HTTPPort: lr.HTTPPort}, true, nil
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
