package runtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

type listReply struct {
	Subjects []struct {
		SubjectID string `json:"subject_id"`
		Status    string `json:"status"`
		Pending   int    `json:"pending"`
	} `json:"subjects"`
	HTTPPort int `json:"http_port"`
}

type pendingReply struct {
	Questions []struct {
		QuestionID int64  `json:"question_id"`
		Header     string `json:"header"`
		Payload    string `json:"payload"`
	} `json:"questions"`
}

type answerReply struct {
	Idled bool `json:"idled"`
}

func resolveAwaiting(c *cobra.Command, d cmd.Deps, scope string) (string, error) {
	body, _ := json.Marshal(map[string]string{"scope": scope})
	r, err := d.NewClient().Do(c.Context(), daemon.Envelope{Op: interaction.OpList, Scope: scope, Body: body})
	if err != nil {
		return "", err
	}
	if !r.OK {
		return "", errors.New(r.Error)
	}
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		return "", err
	}
	awaiting := ""
	for _, s := range lr.Subjects {
		if s.Status == interaction.StatusAwaiting {
			if awaiting != "" {
				return "", errors.New("multiple awaiting subjects in scope; the answer command resolves a single subject")
			}
			awaiting = s.SubjectID
		}
	}
	if awaiting == "" {
		return "", errors.New("no awaiting subject in scope; nothing to answer")
	}
	return awaiting, nil
}

func resolveQuestion(c *cobra.Command, d cmd.Deps, scope, subjectID string, want int64) (int64, error) {
	if want != 0 {
		return want, nil
	}
	body, _ := json.Marshal(map[string]string{"subject_id": subjectID})
	r, err := d.NewClient().Do(c.Context(), daemon.Envelope{Op: interaction.OpPending, Scope: scope, Body: body})
	if err != nil {
		return 0, err
	}
	if !r.OK {
		return 0, errors.New(r.Error)
	}
	var pr pendingReply
	if err := json.Unmarshal(r.Body, &pr); err != nil {
		return 0, err
	}
	if len(pr.Questions) == 0 {
		return 0, errors.New("no open question for this subject")
	}
	if len(pr.Questions) > 1 {
		return 0, fmt.Errorf("%d open questions; pass --question to choose one", len(pr.Questions))
	}
	return pr.Questions[0].QuestionID, nil
}

// AnswerCmd is the non-interactive sibling of the TUI submit: it resolves the
// awaiting subject in a scope, picks the open question, and submits the human's
// answer, printing whether the subject idled.
func AnswerCmd(d cmd.Deps) *cobra.Command {
	var (
		cwd      string
		question int64
		selected []string
		other    string
		notes    string
	)
	c := &cobra.Command{
		Use:    "answer",
		Short:  "Answer the awaiting question for a scope (non-interactive)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := d.EnsureCurrent(c.Context()); err != nil {
				return err
			}
			scope := mustCwd(cwd)
			subjectID, err := resolveAwaiting(c, d, scope)
			if err != nil {
				return err
			}
			questionID, err := resolveQuestion(c, d, scope, subjectID, question)
			if err != nil {
				return err
			}
			body, _ := json.Marshal(interaction.AnswerPayload{
				SubjectID:  subjectID,
				QuestionID: questionID,
				Selected:   selected,
				Other:      other,
				Notes:      notes,
			})
			r, err := d.NewClient().Do(c.Context(), daemon.Envelope{Op: interaction.OpAnswer, Scope: scope, Body: body})
			if err != nil {
				return err
			}
			if !r.OK {
				return errors.New(r.Error)
			}
			var ar answerReply
			if err := json.Unmarshal(r.Body, &ar); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "answered question %d on %s (idled: %t)\n", questionID, subjectID, ar.Idled)
			return nil
		},
	}
	c.Flags().StringVar(&cwd, "cwd", "", "working directory / scope (defaults to the current directory)")
	c.Flags().Int64Var(&question, "question", 0, "question id to answer (defaults to the single open question)")
	c.Flags().StringArrayVar(&selected, "select", nil, "selected option label (repeatable)")
	c.Flags().StringVar(&other, "other", "", "free-text 'other' answer")
	c.Flags().StringVar(&notes, "notes", "", "additional notes for the answer")
	return c
}
