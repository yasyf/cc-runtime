package runtime

import (
	"encoding/json"
	"io"

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
			}
			if message == "" {
				return nil
			}
			if err := d.EnsureCurrentIfRunning(); err != nil {
				return nil
			}
			body, _ := json.Marshal(interaction.NotificationPayload{Message: message})
			_, _ = d.NewClient().Do(c.Context(), daemon.Envelope{
				Op:        interaction.OpCaptureNotification,
				Session:   in.SessionID,
				ClaudePID: d.ClaudePID(),
				Scope:     mustCwd(in.Cwd),
				Body:      body,
			})
			return nil
		},
	}
	c.Flags().StringVar(&source, "source", "notification", "notification source: push (PushNotification tool) or notification (Notification hook)")
	return c
}
