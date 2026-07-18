package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-runtime/interaction"
)

// fakeExeOnPath writes an executable named program into a temp dir and prepends
// that dir to PATH for the test, so exec.LookPath resolves to it. It returns the
// resolved absolute path the wrap argv[0] must equal.
func fakeExeOnPath(t *testing.T, program string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, program)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", program, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestAssembleWrapInjectsSteerFlagsAfterHead(t *testing.T) {
	claude := fakeExeOnPath(t, "claude")

	plan, err := assembleWrap(
		[]string{"claude", "--dangerously-skip-permissions"},
		[]string{"CLAUDE_CODE_SESSION_ID=abc", "PATH=/usr/bin"},
	)
	if err != nil {
		t.Fatalf("assembleWrap: %v", err)
	}

	if plan.path != claude {
		t.Fatalf("plan.path = %q, want the LookPath-resolved %q", plan.path, claude)
	}

	// Wrap's flags land immediately after the claude head, ahead of the caller's
	// own args, so they precede any positional prompt.
	want := []string{
		claude,
		"--disallowedTools", "AskUserQuestion,PushNotification",
		"--append-system-prompt", wrapSteer,
		"--dangerously-skip-permissions",
	}
	if !slices.Equal(plan.argv, want) {
		t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, want)
	}

	// The steer the agent receives must carry the detection sentinel.
	if !strings.Contains(wrapSteer, interaction.WrapSentinel) {
		t.Fatalf("wrapSteer missing WrapSentinel %q:\n%s", interaction.WrapSentinel, wrapSteer)
	}

	// The caller's own arg survives unchanged.
	if !slices.Contains(plan.argv, "--dangerously-skip-permissions") {
		t.Fatalf("passthrough arg dropped; got %#v", plan.argv)
	}

	// The base environment propagates and the sentinel env var is appended.
	wantEnv := interaction.WrapEnvVar + "=" + interaction.WrapSentinel
	if !slices.Contains(plan.env, wantEnv) {
		t.Fatalf("env missing %q; got %v", wantEnv, plan.env)
	}
	if !slices.Contains(plan.env, "CLAUDE_CODE_SESSION_ID=abc") {
		t.Fatalf("env dropped the inherited CLAUDE_CODE_SESSION_ID; got %v", plan.env)
	}
}

// TestAssembleWrapStripsLauncherSeparator proves the composable launcher form
// `cc-runtime wrap -- claude …` (cobra hands the leading "--" straight through
// under DisableFlagParsing) assembles the same argv as the bare form.
func TestAssembleWrapStripsLauncherSeparator(t *testing.T) {
	fakeExeOnPath(t, "claude")

	withDash, err := assembleWrap([]string{"--", "claude", "-p", "hi"}, nil)
	if err != nil {
		t.Fatalf("assembleWrap with leading --: %v", err)
	}
	bare, err := assembleWrap([]string{"claude", "-p", "hi"}, nil)
	if err != nil {
		t.Fatalf("assembleWrap bare: %v", err)
	}
	if !slices.Equal(withDash.argv, bare.argv) {
		t.Fatalf("leading -- changed the argv:\n  %#v\nvs\n  %#v", withDash.argv, bare.argv)
	}
}

// TestAssembleWrapPreservesOrchestratorFlags pins the composed case: cc-orchestrate
// spawns `claude --session-id … --channels … --settings … --append-system-prompt …
// <prompt>`, and wrap must leave --settings, --channels, --session-id, and the
// positional prompt intact while folding its own steering in — the brief survives
// merged because claude's --append-system-prompt is last-wins.
func TestAssembleWrapPreservesOrchestratorFlags(t *testing.T) {
	claude := fakeExeOnPath(t, "claude")

	settings := `{"hooks":{"SessionStart":[]}}`
	plan, err := assembleWrap([]string{
		"claude",
		"--session-id", "sid-1",
		"--channels", "chan-xyz",
		"--settings", settings,
		"--append-system-prompt", "ORCH-BRIEF",
		"the task prompt",
	}, nil)
	if err != nil {
		t.Fatalf("assembleWrap: %v", err)
	}

	want := []string{
		claude,
		"--disallowedTools", "AskUserQuestion,PushNotification",
		"--append-system-prompt", "ORCH-BRIEF\n\n" + wrapSteer,
		"--session-id", "sid-1",
		"--channels", "chan-xyz",
		"--settings", settings,
		"the task prompt",
	}
	if !slices.Equal(plan.argv, want) {
		t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, want)
	}
}

func TestAssembleWrapMergesDisallowedTools(t *testing.T) {
	cases := []struct {
		id         string
		flags      []string
		want       string
		wantPrompt string
	}{
		{"absent", nil, "AskUserQuestion,PushNotification", wrapSteer},
		{"space-form", []string{"--disallowedTools", "Foo,Bar"}, "Foo,Bar,AskUserQuestion,PushNotification", wrapSteer},
		{"equals-form", []string{"--disallowedTools=Foo"}, "Foo,AskUserQuestion,PushNotification", wrapSteer},
		{"alias", []string{"--disallowed-tools", "Foo"}, "Foo,AskUserQuestion,PushNotification", wrapSteer},
		{"already-present", []string{"--disallowedTools", "AskUserQuestion,Foo"}, "AskUserQuestion,Foo,PushNotification", wrapSteer},
		{"space-separated-value", []string{"--disallowedTools", "Foo Bar"}, "Foo,Bar,AskUserQuestion,PushNotification", wrapSteer},
		{"matcher-with-space", []string{"--disallowedTools", "Bash(git *)"}, "Bash(git *),AskUserQuestion,PushNotification", wrapSteer},
		{"variadic-multi-token", []string{"--disallowedTools", "Foo", "Bar"}, "Foo,Bar,AskUserQuestion,PushNotification", wrapSteer},
		{"variadic-stops-at-flag", []string{"--disallowedTools", "Foo", "--append-system-prompt", "P"}, "Foo,AskUserQuestion,PushNotification", "P\n\n" + wrapSteer},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			claude := fakeExeOnPath(t, "claude")
			plan, err := assembleWrap(append([]string{"claude"}, tc.flags...), nil)
			if err != nil {
				t.Fatalf("assembleWrap: %v", err)
			}
			want := []string{
				claude,
				"--disallowedTools", tc.want,
				"--append-system-prompt", tc.wantPrompt,
			}
			if !slices.Equal(plan.argv, want) {
				t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, want)
			}
		})
	}
}

// TestAssembleWrapRejectsUnbalancedParens pins the fail-loud contract: an
// unbalanced paren in a caller --disallowedTools value would otherwise swallow
// every following separator and collapse deny entries into one malformed rule.
// The error must never echo the value — it can carry secrets and main.go prints
// it to stderr, so a leak lands in launcher logs.
func TestAssembleWrapRejectsUnbalancedParens(t *testing.T) {
	fakeExeOnPath(t, "claude")
	const secret = "Authorization: Bearer sk-test-secret"
	for _, tc := range []struct {
		id, value string
	}{
		{"unclosed open paren", "Bash(git *," + secret},
		{"stray close paren", "Bash)git *," + secret},
	} {
		t.Run(tc.id, func(t *testing.T) {
			_, err := assembleWrap([]string{"claude", "--disallowedTools", tc.value}, nil)
			if err == nil || err.Error() != "wrap: unbalanced parentheses in --disallowedTools value" {
				t.Fatalf("err = %v, want the fixed value-free unbalanced-parentheses error", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("err = %v, want the caller value absent", err)
			}
		})
	}
}

func TestAssembleWrapStopsAtOptionTerminator(t *testing.T) {
	claude := fakeExeOnPath(t, "claude")
	cases := []struct {
		id    string
		flags []string
		want  []string
	}{
		{
			"managed-flag-after-terminator",
			[]string{"--", "--disallowedTools", "foo"},
			[]string{
				claude,
				"--disallowedTools", "AskUserQuestion,PushNotification",
				"--append-system-prompt", wrapSteer,
				"--", "--disallowedTools", "foo",
			},
		},
		{
			"managed-flags-before-and-after-terminator",
			[]string{
				"--disallowedTools", "Foo",
				"--",
				"--disallowedTools", "Bar",
				"--append-system-prompt", "positional prompt",
			},
			[]string{
				claude,
				"--disallowedTools", "Foo,AskUserQuestion,PushNotification",
				"--append-system-prompt", wrapSteer,
				"--",
				"--disallowedTools", "Bar",
				"--append-system-prompt", "positional prompt",
			},
		},
		{
			"positional-only-after-terminator",
			[]string{"--", "somepositional"},
			[]string{
				claude,
				"--disallowedTools", "AskUserQuestion,PushNotification",
				"--append-system-prompt", wrapSteer,
				"--", "somepositional",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			plan, err := assembleWrap(append([]string{"claude"}, tc.flags...), nil)
			if err != nil {
				t.Fatalf("assembleWrap: %v", err)
			}
			if !slices.Equal(plan.argv, tc.want) {
				t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, tc.want)
			}
		})
	}
}

// TestAssembleWrapCCPRunInjectsAfterSubcommand proves the `ccp run` launcher —
// which exec's claude and forwards every later arg — gets wrap's flags after the
// run subcommand, and the trailing claude flags survive.
func TestAssembleWrapCCPRunInjectsAfterSubcommand(t *testing.T) {
	ccp := fakeExeOnPath(t, "ccp")

	plan, err := assembleWrap([]string{"ccp", "run", "--session-id", "sid-1"}, nil)
	if err != nil {
		t.Fatalf("assembleWrap: %v", err)
	}

	want := []string{
		ccp, "run",
		"--disallowedTools", "AskUserQuestion,PushNotification",
		"--append-system-prompt", wrapSteer,
		"--session-id", "sid-1",
	}
	if !slices.Equal(plan.argv, want) {
		t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, want)
	}
}

func TestAssembleWrapFailsLoud(t *testing.T) {
	cases := []struct {
		id      string
		args    []string
		wantErr string
	}{
		{"empty-after-separator", []string{"--"}, "missing child command"},
		{"unknown-executable", []string{"--", "bash", "-c", "echo hi"}, "unrecognized child executable"},
		{"ccp-without-run", []string{"ccp", "status"}, "ccp run"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			_, err := assembleWrap(tc.args, nil)
			if err == nil {
				t.Fatalf("assembleWrap(%#v) = nil error, want a loud failure", tc.args)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestAssembleWrapUnresolvableExecutableErrorsLoudly covers a recognized launcher
// name that exec.LookPath cannot resolve.
func TestAssembleWrapUnresolvableExecutableErrorsLoudly(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := assembleWrap([]string{"claude"}, nil)
	if err == nil {
		t.Fatal("assembleWrap with an unresolvable claude returned nil error, want a loud LookPath failure")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Fatalf("error = %v, want it to name the unresolved program", err)
	}
}
