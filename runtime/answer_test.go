package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/cmd"

	"github.com/yasyf/cc-runtime/interaction"
)

// answerDeps builds a Deps that talks to the e2e daemon's socket with no
// upgrade/launch side effects, so AnswerCmd runs against the live daemon.
func (e *e2e) answerDeps() cmd.Deps {
	return cmd.Deps{
		Paths:         interaction.AppPaths(),
		NewClient:     e.launcher.NewClient,
		EnsureCurrent: func(context.Context) error { return nil },
	}
}

// runAnswer drives the non-interactive answer command end to end against the
// e2e daemon, returning its stdout and any error.
func (e *e2e) runAnswer(args ...string) (string, error) {
	e.t.Helper()
	c := AnswerCmd(e.answerDeps())
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(append([]string{"--cwd", e2eScope}, args...))
	err := c.ExecuteContext(context.Background())
	return out.String(), err
}

// TestAnswerCmdRejectsEmptyAnswer asserts the non-interactive answer command
// refuses to submit an empty answer to an options question: no OpAnswer fires, so
// the subject stays awaiting and the gate stays closed. A subsequent --select
// submits and idles the subject.
func TestAnswerCmdRejectsEmptyAnswer(t *testing.T) {
	e := newE2E(t)

	subjectID, _ := e.ask(e2eQuestion("deploy?"))
	if got := e.status(subjectID); got != interaction.StatusAwaiting {
		t.Fatalf("status after ask = %q, want %q", got, interaction.StatusAwaiting)
	}

	// An answer with no --select/--other/--notes must be refused.
	out, err := e.runAnswer()
	if err == nil {
		t.Fatalf("empty answer should be rejected, got success: %q", out)
	}
	if !strings.Contains(err.Error(), "empty answer") {
		t.Fatalf("rejection error = %q, want it to mention an empty answer", err.Error())
	}
	if got := e.status(subjectID); got != interaction.StatusAwaiting {
		t.Fatalf("status after refused empty answer = %q, want still %q (gate stays closed)", got, interaction.StatusAwaiting)
	}
	assertBlocks(t, e, "after a refused empty answer")

	// A real selection submits and idles the subject.
	if _, err := e.runAnswer("--select", "yes"); err != nil {
		t.Fatalf("answering with a selection failed: %v", err)
	}
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after a real answer = %q, want %q", got, interaction.StatusIdle)
	}
	assertAllows(t, e, "after a real answer")
}

// TestAnswerCmdAllowsNotesOnlyAnswer asserts a notes-only answer is accepted on
// an options question: a deliberate free-text reply is valid even with no option
// picked.
func TestAnswerCmdAllowsNotesOnlyAnswer(t *testing.T) {
	e := newE2E(t)

	subjectID, _ := e.ask(e2eQuestion("deploy?"))
	if _, err := e.runAnswer("--notes", "let me think"); err != nil {
		t.Fatalf("notes-only answer should be accepted: %v", err)
	}
	if got := e.status(subjectID); got != interaction.StatusIdle {
		t.Fatalf("status after notes-only answer = %q, want %q", got, interaction.StatusIdle)
	}
}
