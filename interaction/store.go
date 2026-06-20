package interaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PendingQuestion is one open question projected for the agent or TUI.
type PendingQuestion struct {
	QuestionID int64  `json:"question_id"`
	Header     string `json:"header,omitempty"`
	Payload    string `json:"payload"`
}

// ListedSubject is one scope-resolved subject for the TUI, with its open count.
type ListedSubject struct {
	SubjectID string `json:"subject_id"`
	Status    string `json:"status"`
	Pending   int    `json:"pending"`
}

func unix(t time.Time) int64 { return t.UnixMilli() }

// insertPendingAndAwait projects a new question and flips the subject to
// awaiting in one transaction so the gate signal and the projection move
// together.
func insertPendingAndAwait(ctx context.Context, db *sql.DB, subjectID string, questionID int64, header, payload string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ask tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pending_questions(subject_id, question_id, header, payload, created_at) VALUES(?,?,?,?,?)`,
		subjectID, questionID, header, payload, unix(time.Now())); err != nil {
		return fmt.Errorf("insert pending question: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE subjects SET status=?, updated_at=? WHERE id=?`,
		StatusAwaiting, unix(time.Now()), subjectID); err != nil {
		return fmt.Errorf("await subject: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ask tx: %w", err)
	}
	return nil
}

// recordAnswer marks a question answered and, when it was the last open one,
// flips the subject back to idle — both in one transaction so a concurrent ask
// cannot interleave and leak the gate. The returned bool reports whether the
// subject idled.
func recordAnswer(ctx context.Context, db *sql.DB, subjectID string, questionID int64, answer string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin answer tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE pending_questions SET answered=1, answer=? WHERE subject_id=? AND question_id=?`,
		answer, subjectID, questionID); err != nil {
		return false, fmt.Errorf("mark answered: %w", err)
	}
	var open int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM pending_questions WHERE subject_id=? AND answered=0`, subjectID).Scan(&open); err != nil {
		return false, fmt.Errorf("count open questions: %w", err)
	}
	idled := open == 0
	if idled {
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET status=?, updated_at=? WHERE id=?`,
			StatusIdle, unix(time.Now()), subjectID); err != nil {
			return false, fmt.Errorf("idle subject: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit answer tx: %w", err)
	}
	return idled, nil
}

// pollAnswer reads a single question's answered state without ever blocking.
func pollAnswer(ctx context.Context, db *sql.DB, subjectID string, questionID int64) (bool, string, error) {
	var (
		answered int
		answer   string
	)
	err := db.QueryRowContext(ctx,
		`SELECT answered, answer FROM pending_questions WHERE subject_id=? AND question_id=?`,
		subjectID, questionID).Scan(&answered, &answer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("poll answer: %w", err)
	}
	return answered == 1, answer, nil
}

// openQuestions lists a subject's unanswered questions, oldest first.
func openQuestions(ctx context.Context, db *sql.DB, subjectID string) ([]PendingQuestion, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT question_id, header, payload FROM pending_questions WHERE subject_id=? AND answered=0 ORDER BY question_id ASC`,
		subjectID)
	if err != nil {
		return nil, fmt.Errorf("list pending questions: %w", err)
	}
	defer rows.Close()
	out := []PendingQuestion{}
	for rows.Next() {
		var q PendingQuestion
		if err := rows.Scan(&q.QuestionID, &q.Header, &q.Payload); err != nil {
			return nil, fmt.Errorf("scan pending question: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// listSubjects returns the scope's active subjects with their open-question
// counts for the TUI.
func listSubjects(ctx context.Context, db *sql.DB, scope string) ([]ListedSubject, error) {
	rows, err := db.QueryContext(ctx, `
SELECT s.id, s.status, COUNT(p.question_id)
FROM subjects s
LEFT JOIN pending_questions p ON p.subject_id = s.id AND p.answered = 0
WHERE s.scope = ? AND s.status IN (?, ?)
GROUP BY s.id, s.status
ORDER BY s.created_at DESC, s.rowid DESC`,
		scope, StatusIdle, StatusAwaiting)
	if err != nil {
		return nil, fmt.Errorf("list subjects: %w", err)
	}
	defer rows.Close()
	out := []ListedSubject{}
	for rows.Next() {
		var ls ListedSubject
		if err := rows.Scan(&ls.SubjectID, &ls.Status, &ls.Pending); err != nil {
			return nil, fmt.Errorf("scan listed subject: %w", err)
		}
		out = append(out, ls)
	}
	return out, rows.Err()
}
