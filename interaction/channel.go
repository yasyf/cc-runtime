package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"
)

// notifyMethod is the JSON-RPC method each subject event is pushed under on the
// Claude channel; it drives Claude Code's <channel> rendering and must not be a
// cc-runtime-specific name.
const notifyMethod = "notifications/claude/channel"

// channelInstructions steers the agent to cc-runtime's remotely-answerable
// ask/notify over the native AskUserQuestion/PushNotification tools, and marks
// the channel attach handshake as status, not a request.
const channelInstructions = `This MCP server is the cc-runtime interaction channel: it backs Claude Code's harness-injected AskUserQuestion and PushNotification with remote delivery and durable, full-context questions.

Use the cc-runtime ask tool instead of the native AskUserQuestion tool, and the cc-runtime notify tool instead of PushNotification. The native AskUserQuestion renders only in the terminal and cannot be answered remotely; a cc-runtime ask reaches the human on the web app or phone, persists for the session, and blocks your edits until they respond, so you never proceed past an unanswered question.

A channel.hello tag arrives once when the channel attaches, and a channel.changed tag marks a connection-presence change. Both are status signals that confirm the channel is live. They carry no task and need no reply, narration, or tool call. When one arrives, continue whatever you were doing. If the conversation so far holds nothing but this handshake, there is no request yet, so wait for the human instead of asking what they want.`

// wrappedInstructions is the trimmed instructions returned when the session was
// launched via `cc-runtime wrap`. The wrap launch already removed the native
// AskUserQuestion/PushNotification and steered the agent to ask/notify in its
// appended system prompt, so the soft-steer in channelInstructions would be
// redundant. Only the channel-hello/status guidance remains.
const wrappedInstructions = `This MCP server is the cc-runtime interaction channel: its ask and notify tools reach the human on the web app or phone, persist for the session, and (for ask) block your edits until the human responds.

A channel.hello tag arrives once when the channel attaches, and a channel.changed tag marks a connection-presence change. Both are status signals that confirm the channel is live. They carry no task and need no reply, narration, or tool call. When one arrives, continue whatever you were doing. If the conversation so far holds nothing but this handshake, there is no request yet, so wait for the human instead of asking what they want.`

// askPollInterval paces the inline long-poll between OpAnswerPoll round-trips.
const askPollInterval = 750 * time.Millisecond

// askPollTimeout bounds each individual OpAnswerPoll round-trip.
const askPollTimeout = 5 * time.Second

// askBudget bounds the whole inline long-poll. On expiry the ask tool returns a
// non-error pending string; the edit gate is the backstop that keeps the agent
// from proceeding.
const askBudget = 4 * time.Minute

func askToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"header":      map[string]any{"type": "string", "description": "short chip labeling the question, e.g. Approach"},
			"prompt":      map[string]any{"type": "string", "description": "the question to ask the human"},
			"reasoning":   map[string]any{"type": "string", "description": "why you are asking and what hinges on the answer"},
			"diff":        map[string]any{"type": "string", "description": "a unified diff or code excerpt the human should weigh"},
			"multiSelect": map[string]any{"type": "boolean", "description": "allow selecting more than one option"},
			"options": map[string]any{
				"type":        "array",
				"description": "the choices, mirroring AskUserQuestion",
				"minItems":    1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label":       map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
						"preview":     map[string]any{"type": "string", "description": "markdown/code shown when the option is focused"},
					},
					"required": []string{"label"},
				},
			},
		},
		"required": []string{"prompt", "options"},
	}
}

func notifyToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "the notification text to deliver to the human"},
			"urgency": map[string]any{"type": "string", "description": "optional urgency hint, e.g. low, normal, high"},
		},
		"required": []string{"message"},
	}
}

func renderAnswer(raw json.RawMessage) string {
	var a AnswerPayload
	if err := json.Unmarshal(raw, &a); err != nil {
		panic(err)
	}
	var b strings.Builder
	if len(a.Selected) > 0 {
		fmt.Fprintf(&b, "selected: %s", strings.Join(a.Selected, ", "))
	}
	if a.Other != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "other: %s", a.Other)
	}
	if a.Notes != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "notes: %s", a.Notes)
	}
	if b.Len() == 0 {
		return "(empty answer)"
	}
	return b.String()
}

func pollOnce(ctx context.Context, client *daemon.Client, session, scope string, pid int, poll pollBody) (pollReply, error) {
	pctx, cancel := context.WithTimeout(ctx, askPollTimeout)
	defer cancel()
	body, _ := json.Marshal(poll)
	r, err := client.Do(pctx, daemon.Envelope{
		Op: OpAnswerPoll, Session: session, ClaudePID: pid, Scope: scope, Body: body,
	})
	if err != nil {
		return pollReply{}, err
	}
	if !r.OK {
		return pollReply{}, fmt.Errorf("%s", r.Error)
	}
	var reply pollReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		return pollReply{}, err
	}
	return reply, nil
}

// awaitAnswer long-polls the daemon for the human's answer, bounded by budget.
// It returns the rendered answer text, or empty when the budget expires before
// an answer arrives.
func awaitAnswer(ctx context.Context, client *daemon.Client, session, scope string, pid int, poll pollBody, budget time.Duration) (string, error) {
	deadline := time.After(budget)
	for {
		reply, err := pollOnce(ctx, client, session, scope, pid, poll)
		if err != nil {
			return "", err
		}
		if reply.Answered {
			return renderAnswer(reply.Answer), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", nil
		case <-time.After(askPollInterval):
		}
	}
}

// ChannelTools advertises cc-runtime's interaction tools to the agent's MCP
// channel. The handlers round-trip to the daemon because the channel server is a
// separate stdio process that cannot Append directly; the ask tool blocks
// inline, long-polling for the human's answer. pid is Claude's window pid (the
// channel server runs as a child of claude, so its own os.Getpid is the wrong
// owner); it stamps ClaudePID on every envelope so subjects bind to the window.
//
// Subject-key invariant: the durable key is (session, scope) — cc-interact's
// resolver enforces UNIQUE(session_id, scope) and CAS-rebinds claude_pid
// toward each envelope's window, which is what lets a resumed claude (same
// session, fresh pid) reconnect to its subject and pending questions. Pid
// therefore must never join the key, and (session, scope) must be unique
// across concurrently-live claudes: two live processes sharing one — e.g. an
// orchestrated agent plus a manual `cc-runtime wrap -- claude --resume
// <session>` in the same scope — rebind the same subject back and forth and
// share its questions and edit-gate state. That launch is unsupported.
func ChannelTools(_ context.Context, session, scope string, pid int) ([]channel.Tool, string, string, error) {
	ask := channel.Tool{
		Name:        "ask",
		Description: "Ask the human a question with structured options; remotely answerable and persisted for the session. Use this instead of the native AskUserQuestion. Blocks your edits until the human responds.",
		InputSchema: askToolSchema(),
		Handler: func(ctx context.Context, args json.RawMessage, _ func(string)) (string, bool) {
			client, err := daemon.NewClient(ctx, daemon.ClientConfig{
				Socket: AppPaths().SocketPath(), WireBuild: daemon.WireBuild,
			})
			if err != nil {
				return "ask failed: " + err.Error(), true
			}
			defer func() { _ = client.Close() }()
			r, err := client.Do(ctx, daemon.Envelope{
				Op: OpAsk, Session: session, ClaudePID: pid, Scope: scope, Body: args,
			})
			if err != nil {
				return "ask failed: " + err.Error(), true
			}
			if !r.OK {
				return r.Error, true
			}
			var ar askReply
			if err := json.Unmarshal(r.Body, &ar); err != nil {
				return "ask failed: " + err.Error(), true
			}
			text, err := awaitAnswer(ctx, client, session, scope, pid, pollBody{SubjectID: ar.SubjectID, QuestionID: ar.QuestionID}, askBudget)
			if err != nil {
				return "ask failed: " + err.Error(), true
			}
			if text == "" {
				return "answer pending — watch the cc-runtime channel for the reply.", false
			}
			return text, false
		},
	}

	notify := channel.Tool{
		Name:        "notify",
		Description: "Send the human a notification, delivered remotely to the web app or phone. Use this instead of PushNotification.",
		InputSchema: notifyToolSchema(),
		Handler: func(ctx context.Context, args json.RawMessage, _ func(string)) (string, bool) {
			client, err := daemon.NewClient(ctx, daemon.ClientConfig{
				Socket: AppPaths().SocketPath(), WireBuild: daemon.WireBuild,
			})
			if err != nil {
				return "notify failed: " + err.Error(), true
			}
			defer func() { _ = client.Close() }()
			r, err := client.Do(ctx, daemon.Envelope{
				Op: OpNotify, Session: session, ClaudePID: pid, Scope: scope, Body: args,
			})
			if err != nil {
				return "notify failed: " + err.Error(), true
			}
			if !r.OK {
				return r.Error, true
			}
			return "delivered", false
		},
	}

	instructions := channelInstructions
	if launchedViaWrap() {
		instructions = wrappedInstructions
	}
	return []channel.Tool{ask, notify}, notifyMethod, instructions, nil
}

// launchedViaWrap reports whether this session was launched via `cc-runtime
// wrap`. It is the single detection seam: the wrap launch sets WrapEnvVar to the
// WrapSentinel in the child environment, and that variable propagates to the
// MCP-launched channel subprocess the same way CLAUDE_CODE_SESSION_ID does.
//
// The transcript was the original source, but a real run proved Claude Code does
// not persist --append-system-prompt content (and so the sentinel) into the
// session .jsonl, so the env var is the detection of record.
func launchedViaWrap() bool {
	return os.Getenv(WrapEnvVar) == WrapSentinel
}
