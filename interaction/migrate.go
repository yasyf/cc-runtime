package interaction

import (
	"context"
	"database/sql"
	"fmt"
)

// schema is the interaction projection layered on cc-interact's core
// subjects/events tables. question_id is the per-subject event seq, which is not
// globally unique, so the primary key is composite (subject_id, question_id).
const schema = `
CREATE TABLE IF NOT EXISTS pending_questions (
  subject_id  TEXT NOT NULL REFERENCES subjects(id),
  question_id INTEGER NOT NULL,
  header      TEXT NOT NULL DEFAULT '',
  payload     TEXT NOT NULL,
  answer      TEXT NOT NULL DEFAULT '',
  answered    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (subject_id, question_id)
);
CREATE INDEX IF NOT EXISTS idx_pending_questions_open ON pending_questions(subject_id, answered);
`

// Migrate applies the interaction projection on top of cc-interact's core:
// idempotent CREATE TABLE IF NOT EXISTS, no migrations beyond it.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply interaction schema: %w", err)
	}
	return nil
}
