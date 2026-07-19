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
	Subjects []interaction.ListedSubject `json:"subjects"`
}

type pendingReply struct {
	Questions []interaction.PendingQuestion `json:"questions"`
}

type answerReply struct {
	Idled bool `json:"idled"`
}

func resolveAwaiting(c *cobra.Command, client *daemon.Client, scope string) (string, error) {
	body, _ := json.Marshal(map[string]string{"scope": scope})
	r, err := client.Do(c.Context(), daemon.Envelope{Op: interaction.OpList, Scope: scope, Body: body})
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

// resolveQuestion picks the open question to answer and returns it whole, so the
// caller can validate the answer against its options before submitting. When
// want is non-zero it selects that question id from the pending set; otherwise it
// requires exactly one open question.
func resolveQuestion(c *cobra.Command, client *daemon.Client, scope, subjectID string, want int64) (interaction.PendingQuestion, error) {
	body, _ := json.Marshal(map[string]string{"subject_id": subjectID})
	r, err := client.Do(c.Context(), daemon.Envelope{Op: interaction.OpPending, Scope: scope, Body: body})
	if err != nil {
		return interaction.PendingQuestion{}, err
	}
	if !r.OK {
		return interaction.PendingQuestion{}, errors.New(r.Error)
	}
	var pr pendingReply
	if err := json.Unmarshal(r.Body, &pr); err != nil {
		return interaction.PendingQuestion{}, err
	}
	if len(pr.Questions) == 0 {
		return interaction.PendingQuestion{}, errors.New("no open question for this subject")
	}
	if want != 0 {
		for _, q := range pr.Questions {
			if q.QuestionID == want {
				return q, nil
			}
		}
		return interaction.PendingQuestion{}, fmt.Errorf("question %d is not open for this subject", want)
	}
	if len(pr.Questions) > 1 {
		return interaction.PendingQuestion{}, fmt.Errorf("%d open questions; pass --question to choose one", len(pr.Questions))
	}
	return pr.Questions[0], nil
}

// hasOptions reports whether the pending question carries selectable options, so
// an answer with no selection is only legitimate when the question is free-text.
func hasOptions(pq interaction.PendingQuestion) (bool, error) {
	var p interaction.QuestionPayload
	if err := json.Unmarshal([]byte(pq.Payload), &p); err != nil {
		return false, fmt.Errorf("decode question payload: %w", err)
	}
	return len(p.Options) > 0, nil
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
			client, err := d.NewClient(c.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			subjectID, err := resolveAwaiting(c, client, scope)
			if err != nil {
				return err
			}
			pq, err := resolveQuestion(c, client, scope, subjectID, question)
			if err != nil {
				return err
			}
			questionID := pq.QuestionID
			withOptions, err := hasOptions(pq)
			if err != nil {
				return err
			}
			if withOptions && len(selected) == 0 && other == "" && notes == "" {
				return fmt.Errorf("question %d has options; pass --select, --other, or --notes (refusing to submit an empty answer)", questionID)
			}
			body, _ := json.Marshal(interaction.AnswerPayload{
				SubjectID:  subjectID,
				QuestionID: questionID,
				Selected:   selected,
				Other:      other,
				Notes:      notes,
			})
			r, err := client.Do(c.Context(), daemon.Envelope{Op: interaction.OpAnswer, Scope: scope, Body: body})
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
