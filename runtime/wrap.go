package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// WrapCmd is the `cc-runtime wrap [--] <claude|ccp run> <args…>` launcher: it
// hard-disables the native AskUserQuestion/PushNotification on the child and steers
// it to the cc-runtime ask/notify MCP tools, then syscall.Exec's the child so it
// inherits the TTY, signals, and environment. `cc-runtime wrap -- <argv…>` composes
// as a prefix on an orchestrated claude invocation, merging its steering flags into
// that argv without clobbering the caller's own flags.
func WrapCmd() *cobra.Command {
	c := &cobra.Command{
		Use:                "wrap [--] <claude|ccp run> [args...]",
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
// launch without exec'ing — the pure seam the unit test drives. It strips a leading
// "--" separator, recognizes the child launcher (bare claude, or `ccp run`),
// resolves it via exec.LookPath, merges wrap's flags in after the invocation head,
// and mirrors the WrapSentinel into CC_RUNTIME_WRAP. An unrecognized executable
// fails loud.
func assembleWrap(args, baseEnv []string) (wrapPlan, error) {
	// cobra's DisableFlagParsing hands the launcher-form separator straight
	// through, so `cc-runtime wrap -- <argv…>` arrives with a leading "--".
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return wrapPlan{}, fmt.Errorf("wrap: missing child command")
	}

	head, err := wrapHead(args)
	if err != nil {
		return wrapPlan{}, err
	}

	path, err := exec.LookPath(args[0])
	if err != nil {
		return wrapPlan{}, fmt.Errorf("wrap: resolve %q: %w", args[0], err)
	}

	child := append([]string(nil), args...)
	child[0] = path

	env := append([]string(nil), baseEnv...)
	env = append(env, interaction.WrapEnvVar+"="+interaction.WrapSentinel)

	argv, err := mergeWrapFlags(child, head)
	if err != nil {
		return wrapPlan{}, err
	}
	return wrapPlan{path: path, argv: argv, env: env}, nil
}

// wrapHead reports how many leading argv tokens form the child's invocation head —
// the executable, plus a subcommand when the executable itself exec's claude. Wrap's
// flags are injected immediately after the head so they reach claude ahead of any
// positional prompt. Only claude and `ccp run` are recognized; wrap refuses to guess
// where flags belong for any other launcher.
func wrapHead(args []string) (int, error) {
	switch filepath.Base(args[0]) {
	case "claude":
		return 1, nil
	case "ccp":
		if len(args) < 2 || args[1] != "run" {
			return 0, fmt.Errorf("wrap: ccp child must be `ccp run …` (run exec's claude and forwards its args); got %q", strings.Join(args, " "))
		}
		return 2, nil
	default:
		return 0, fmt.Errorf("wrap: unrecognized child executable %q; wrap injects claude flags and only recognizes claude or `ccp run`", args[0])
	}
}

// mergeWrapFlags folds wrap's --disallowedTools and --append-system-prompt into the
// child argv without clobbering the caller's own flags. A managed flag the child
// already carries is merged value-wise — disallowed tools are unioned, wrap's steer
// is appended to the caller's system prompt — rather than duplicated: claude's
// --append-system-prompt is last-wins, so a second flag would silently drop the
// caller's prompt. Every other flag (--settings, --channels, --session-id) and the
// positional prompt ride through untouched in their original order.
func mergeWrapFlags(child []string, head int) ([]string, error) {
	tail := child[head:]

	var disallowed []string
	appendPrompt := ""
	rest := make([]string, 0, len(tail))
	for i := 0; i < len(tail); i++ {
		if tail[i] == "--" {
			rest = append(rest, tail[i:]...)
			break
		}
		name, val, inline := splitWrapFlag(tail[i])
		switch name {
		case "--disallowedTools", "--disallowed-tools":
			if inline {
				disallowed = append(disallowed, val)
				continue
			}
			// claude's flag is variadic (<tools...>): the space form owns every
			// following token up to the next flag.
			for i+1 < len(tail) && !strings.HasPrefix(tail[i+1], "-") {
				i++
				disallowed = append(disallowed, tail[i])
			}
		case "--append-system-prompt":
			if !inline && i+1 < len(tail) && tail[i+1] != "--" {
				i++
				val = tail[i]
			}
			appendPrompt = val
		default:
			rest = append(rest, tail[i])
		}
	}

	merged, err := mergeDisallowedTools(disallowed)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(child)+4)
	out = append(out, child[:head]...)
	out = append(out, "--disallowedTools", merged)
	out = append(out, "--append-system-prompt", mergeAppendPrompt(appendPrompt))
	out = append(out, rest...)
	return out, nil
}

// splitWrapFlag splits a `--name=value` token into its parts; a `--name` token
// (value in the following argv element) reports inline false, and a non-flag token
// reports itself as the name.
func splitWrapFlag(tok string) (name, val string, inline bool) {
	if !strings.HasPrefix(tok, "--") {
		return tok, "", false
	}
	if i := strings.IndexByte(tok, '='); i >= 0 {
		return tok[:i], tok[i+1:], true
	}
	return tok, "", false
}

// mergeDisallowedTools unions the caller's disallowed-tool values (each a comma- or
// space-separated list, per claude) with wrap's own two, preserving first-seen order
// and dropping duplicates. A malformed caller value fails loud.
func mergeDisallowedTools(existing []string) (string, error) {
	seen := map[string]bool{}
	var tools []string
	add := func(list string) error {
		split, err := splitToolList(list)
		if err != nil {
			return err
		}
		for _, t := range split {
			if !seen[t] {
				seen[t] = true
				tools = append(tools, t)
			}
		}
		return nil
	}
	for _, e := range existing {
		if err := add(e); err != nil {
			return "", err
		}
	}
	if err := add(wrapDisallowedTools); err != nil {
		return "", err
	}
	return strings.Join(tools, ","), nil
}

// splitToolList splits a comma- or space-separated tool list, keeping separators
// inside parentheses as part of the matcher — `Bash(git *)` is one entry, per
// claude's own list syntax. Unbalanced parentheses error: silently swallowing
// every following separator would collapse deny entries into one malformed rule.
// The error never echoes the caller-controlled value — it lands in launcher logs.
func splitToolList(list string) ([]string, error) {
	var out []string
	depth, start := 0, 0
	for i, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, errors.New("wrap: unbalanced parentheses in --disallowedTools value")
			}
		case ',', ' ':
			if depth == 0 {
				if i > start {
					out = append(out, list[start:i])
				}
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, errors.New("wrap: unbalanced parentheses in --disallowedTools value")
	}
	if start < len(list) {
		out = append(out, list[start:])
	}
	return out, nil
}

// mergeAppendPrompt appends wrap's steer to the caller's system prompt so both
// survive in the single flag claude honors.
func mergeAppendPrompt(existing string) string {
	if existing == "" {
		return wrapSteer
	}
	return existing + "\n\n" + wrapSteer
}
