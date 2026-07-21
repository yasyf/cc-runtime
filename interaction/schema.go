package interaction

import "github.com/yasyf/cc-interact/store"

// StoreSchema is the exact interaction projection layered on cc-interact's
// v1 subjects and events tables.
var StoreSchema = store.Schema{DDL: `
CREATE TABLE pending_questions (
  subject_id  TEXT NOT NULL REFERENCES subjects(id),
  question_id INTEGER NOT NULL,
  header      TEXT NOT NULL DEFAULT '',
  payload     TEXT NOT NULL,
  answer      TEXT NOT NULL DEFAULT '',
  answered    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (subject_id, question_id)
);
CREATE INDEX idx_pending_questions_open ON pending_questions(subject_id, answered);
`}
