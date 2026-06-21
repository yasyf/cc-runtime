package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-runtime/interaction"
)

// wrapDisallowedTools is the comma-separated list `cc-runtime wrap` hands
// claude's --disallowedTools flag: bare tool names remove AskUserQuestion and
// PushNotification from the model's context, so the agent cannot reach the native
// in-terminal tools and falls through to the cc-runtime ask/notify MCP tools.
const wrapDisallowedTools = "AskUserQuestion,PushNotification"

// wrapSteer is the system-prompt addition `cc-runtime wrap` appends to the child
// claude. It tells the agent the native AskUserQuestion/PushNotification are gone
// and to use the cc-runtime ask/notify MCP tools. It embeds the WrapSentinel for
// provenance, though the MCP channel detects the wrap launch from the
// CC_RUNTIME_WRAP env var, not this prompt. Voice is aligned with
// interaction.channelInstructions.
var wrapSteer = fmt.Sprintf(`%s

Claude Code's native AskUserQuestion and PushNotification are unavailable in this session: they have been removed from your tool set. Use the cc-runtime ask tool in place of AskUserQuestion, and the cc-runtime notify tool in place of PushNotification. The cc-runtime ask reaches the human on the web app or phone, persists for the session, and blocks your edits until they respond, so you never proceed past an unanswered question.`,
	interaction.WrapSentinel)

// wrapPlan is the resolved program, child argv, and environment a wrap launch
// will exec into. It is assembled by assembleWrap (pure, unit-testable) and
// handed to syscall.Exec by the command's RunE.
type wrapPlan struct {
	path string
	argv []string
	env  []string
}

// WrapCmd is the `cc-runtime wrap <program> <args…>` passthrough: it hard-disables
// the native AskUserQuestion/PushNotification on the child and steers it to the
// cc-runtime ask/notify MCP tools, then replaces this process with the child via
// syscall.Exec so the child inherits the TTY, signals, and environment.
func WrapCmd() *cobra.Command {
	c := &cobra.Command{
		Use:                "wrap [program] [args...]",
		Short:              "Run claude with the native ask/notify tools disabled and steered to cc-runtime",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			plan, err := assembleWrap(args, os.Environ())
			if err != nil {
				return err
			}
			return syscall.Exec(plan.path, plan.argv, plan.env)
		},
	}
	return c
}

// assembleWrap builds the child program path, argv, and environment for a wrap
// launch without exec'ing — the pure seam the unit test drives. args[0] is the
// passthrough program (resolved via exec.LookPath), args[1:] are the user's own
// claude args, which pass through unchanged. It appends the two steering flags
// (--disallowedTools, --append-system-prompt) after the user's args, and mirrors
// the WrapSentinel into CC_RUNTIME_WRAP in the returned environment.
func assembleWrap(args, baseEnv []string) (wrapPlan, error) {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return wrapPlan{}, fmt.Errorf("wrap: resolve %q: %w", args[0], err)
	}

	argv := make([]string, 0, len(args)+4)
	argv = append(argv, path)
	argv = append(argv, args[1:]...)
	argv = append(argv,
		"--disallowedTools", wrapDisallowedTools,
		"--append-system-prompt", wrapSteer,
	)

	env := append([]string(nil), baseEnv...)
	env = append(env, interaction.WrapEnvVar+"="+interaction.WrapSentinel)

	return wrapPlan{path: path, argv: argv, env: env}, nil
}
