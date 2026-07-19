package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

// notifyHookInput is the subset of a Claude Code hook's stdin JSON the
// capture-notification hook reads. cc-interact's own hookInput is unexported and
// carries no message field, so this is a local mirror with one.
type notifyHookInput struct {
	SessionID string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Message   string          `json:"message"`
}

func readNotifyHookInput(r io.Reader) notifyHookInput {
	b, err := io.ReadAll(r)
	if err != nil || len(b) == 0 {
		return notifyHookInput{}
	}
	var in notifyHookInput
	_ = json.Unmarshal(b, &in)
	return in
}

func pushMessage(toolInput json.RawMessage) string {
	if len(toolInput) == 0 {
		return ""
	}
	var ti struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(toolInput, &ti)
	return ti.Message
}

// askToolInput mirrors the native AskUserQuestion PreToolUse tool_input: a
// questions array (1-4), each carrying the question text under "question", a
// short header chip, a multiSelect flag, and label/description options. The
// field names and the array wrapper are confirmed against Claude Code's
// documented AskUserQuestion schema. Tolerant of a missing/empty wrapper: an
// agent emitting a single bare question still maps via the flat fields.
type askToolInput struct {
	Questions []nativeQuestion `json:"questions"`
	nativeQuestion
}

// nativeQuestion is one native AskUserQuestion entry. It carries both the native
// prompt key ("question") and cc-runtime's own ("prompt") so the mapping is
// tolerant of either; askQuestion folds them.
type nativeQuestion struct {
	Header      string               `json:"header"`
	Question    string               `json:"question"`
	Prompt      string               `json:"prompt"`
	MultiSelect bool                 `json:"multiSelect"`
	Options     []interaction.Option `json:"options"`
}

// askQuestion decodes a native AskUserQuestion tool_input into the cc-runtime
// QuestionPayload. It captures the first question in the array (the feed mirror
// is a visibility breadcrumb, not the full interactive picker); when no array
// wrapper is present it falls back to the flat top-level fields.
func askQuestion(toolInput json.RawMessage) (interaction.QuestionPayload, bool) {
	if len(toolInput) == 0 {
		return interaction.QuestionPayload{}, false
	}
	var ti askToolInput
	if err := json.Unmarshal(toolInput, &ti); err != nil {
		return interaction.QuestionPayload{}, false
	}
	nq := ti.nativeQuestion
	if len(ti.Questions) > 0 {
		nq = ti.Questions[0]
	}
	prompt := nq.Question
	if prompt == "" {
		prompt = nq.Prompt
	}
	q := interaction.QuestionPayload{
		Header:      nq.Header,
		Prompt:      prompt,
		MultiSelect: nq.MultiSelect,
		Options:     nq.Options,
	}
	if q.Header == "" && q.Prompt == "" {
		return interaction.QuestionPayload{}, false
	}
	return q, true
}

// CaptureNotificationCmd mirrors a native notification (a PushNotification tool
// call or a Notification hook) into the agent's subject log as a system-origin
// event. It is best-effort and always exits 0 so it never blocks the native
// tool: it upgrades a running daemon but never cold-starts one.
func CaptureNotificationCmd(d cmd.Deps) *cobra.Command {
	var source string
	c := &cobra.Command{
		Use:    "capture-notification",
		Short:  "Mirror a native notification into the agent's subject log",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			in := readNotifyHookInput(c.InOrStdin())
			var message string
			switch source {
			case "push":
				message = pushMessage(in.ToolInput)
			case "notification":
				message = in.Message
			default:
				return fmt.Errorf("unknown notification source %q", source)
			}
			if message == "" {
				return nil
			}
			scope := in.Cwd
			if scope == "" {
				wd, err := os.Getwd()
				if err != nil {
					return nil
				}
				scope = wd
			}
			ctx := c.Context()
			if err := d.EnsureCurrentIfRunning(ctx); err != nil {
				return nil
			}
			body, _ := json.Marshal(interaction.NotificationPayload{Message: message})
			client, err := d.NewClient(ctx)
			if err != nil {
				return nil
			}
			defer func() { _ = client.Close() }()
			_, _ = client.Do(ctx, daemon.Envelope{
				Op:        interaction.OpCaptureNotification,
				Session:   in.SessionID,
				ClaudePID: d.ClaudePID(),
				Scope:     scope,
				Body:      body,
			})
			return nil
		},
	}
	c.Flags().StringVar(&source, "source", "notification", "notification source: push (PushNotification tool) or notification (Notification hook)")
	return c
}

// CaptureAskCmd mirrors a native AskUserQuestion tool call into the agent's
// subject log as a system-origin event. The native picker is synchronous at the
// terminal — the agent is already blocked there and the human answers in the
// terminal — so this is a one-way mirror for durability and remote visibility,
// never an open cc-runtime question: it does not engage the edit gate. It is
// best-effort and always exits 0 so it never blocks the native tool: it upgrades
// a running daemon but never cold-starts one.
func CaptureAskCmd(d cmd.Deps) *cobra.Command {
	c := &cobra.Command{
		Use:    "capture-ask",
		Short:  "Mirror a native AskUserQuestion into the agent's subject log",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			in := readNotifyHookInput(c.InOrStdin())
			q, ok := askQuestion(in.ToolInput)
			if !ok {
				return nil
			}
			scope := in.Cwd
			if scope == "" {
				wd, err := os.Getwd()
				if err != nil {
					return nil
				}
				scope = wd
			}
			ctx := c.Context()
			if err := d.EnsureCurrentIfRunning(ctx); err != nil {
				return nil
			}
			body, _ := json.Marshal(q)
			client, err := d.NewClient(ctx)
			if err != nil {
				return nil
			}
			defer func() { _ = client.Close() }()
			_, _ = client.Do(ctx, daemon.Envelope{
				Op:        interaction.OpCaptureQuestion,
				Session:   in.SessionID,
				ClaudePID: d.ClaudePID(),
				Scope:     scope,
				Body:      body,
			})
			return nil
		},
	}
	return c
}
