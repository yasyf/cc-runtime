package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-runtime/interaction"
)

// fakeClaudeOnPath writes an executable named program into a temp dir and
// prepends that dir to PATH for the test, so exec.LookPath resolves to it. It
// returns the resolved absolute path the wrap argv[0] must equal.
func fakeClaudeOnPath(t *testing.T, program string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, program)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", program, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestAssembleWrapAppendsSteerFlagsAfterPassthroughArgs(t *testing.T) {
	claude := fakeClaudeOnPath(t, "claude")

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

	want := []string{
		claude,
		"--dangerously-skip-permissions",
		"--disallowedTools", "AskUserQuestion,PushNotification",
		"--append-system-prompt", wrapSteer,
	}
	if !slices.Equal(plan.argv, want) {
		t.Fatalf("argv =\n  %#v\nwant\n  %#v", plan.argv, want)
	}

	// The steer the agent receives must carry the detection sentinel.
	if !strings.Contains(wrapSteer, interaction.WrapSentinel) {
		t.Fatalf("wrapSteer missing WrapSentinel %q:\n%s", interaction.WrapSentinel, wrapSteer)
	}

	// The user's own claude args pass through unchanged and ahead of the appended
	// steer flags.
	if plan.argv[1] != "--dangerously-skip-permissions" {
		t.Fatalf("passthrough arg = %q, want it unchanged at argv[1]", plan.argv[1])
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

// TestAssembleWrapExactChildArgvForReportedCase pins the precise child argv the
// report calls out: `cc-runtime wrap claude --dangerously-skip-permissions`.
func TestAssembleWrapExactChildArgvForReportedCase(t *testing.T) {
	claude := fakeClaudeOnPath(t, "claude")

	plan, err := assembleWrap([]string{"claude", "--dangerously-skip-permissions"}, nil)
	if err != nil {
		t.Fatalf("assembleWrap: %v", err)
	}

	// Everything but the steer string (which is long); assert structure + spelling.
	if plan.argv[0] != claude {
		t.Fatalf("argv[0] = %q, want %q", plan.argv[0], claude)
	}
	gotFlags := plan.argv[1:]
	wantFlags := []string{
		"--dangerously-skip-permissions",
		"--disallowedTools", "AskUserQuestion,PushNotification",
		"--append-system-prompt",
	}
	if !slices.Equal(gotFlags[:len(wantFlags)], wantFlags) {
		t.Fatalf("flags = %#v, want prefix %#v", gotFlags, wantFlags)
	}
	if gotFlags[len(wantFlags)] != wrapSteer {
		t.Fatalf("--append-system-prompt value did not equal wrapSteer")
	}
}

func TestAssembleWrapUnknownProgramErrorsLoudly(t *testing.T) {
	// An empty PATH so the bogus program cannot resolve.
	t.Setenv("PATH", "")
	_, err := assembleWrap([]string{"definitely-not-a-real-binary-xyz"}, nil)
	if err == nil {
		t.Fatal("assembleWrap with an unresolvable program returned nil error, want a loud LookPath failure")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-binary-xyz") {
		t.Fatalf("error = %v, want it to name the unresolved program", err)
	}
}
