package interaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sqlite "modernc.org/sqlite"
)

// SQLite result codes for a contended write. They are part of the SQLite ABI
// and stable across platforms, so we name them here rather than pull in the
// heavyweight modernc.org/sqlite/lib generated package for two integers.
const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

// cleanupTimeout bounds the post-Append projection writes. They run on a
// daemon-owned context derived from context.Background, not hc.Ctx (the daemon's
// shared root, cancelled only on daemon SHUTDOWN). That lets the projection of a
// durable question or answer finish even as hc.Ctx is being torn down by
// shutdown, rather than abandoning it mid-projection and wedging the gate.
const cleanupTimeout = 5 * time.Second

// busyRetries bounds the SQLITE_BUSY/LOCKED retry on a post-Append projection
// write. The store serializes writes on a single connection with a busy_timeout,
// so contention is rare; a few quick attempts cover the remaining window.
const busyRetries = 5

// busyBackoff is the pause between projection-write retries on a contended DB.
const busyBackoff = 2 * time.Millisecond

// PendingQuestion is one open question projected for the agent or TUI.
type PendingQuestion struct {
	QuestionID int64  `json:"question_id"`
	Header     string `json:"header,omitempty"`
	Payload    string `json:"payload"`
}

// ListedSubject is one active subject for the TUI and the REST sessions
// listing, with its open count.
type ListedSubject struct {
	SubjectID string `json:"subject_id"`
	Scope     string `json:"scope"`
	Status    string `json:"status"`
	Pending   int    `json:"pending"`
}

func unix(t time.Time) int64 { return t.UnixMilli() }

// cleanupCtx returns a short daemon-owned context for the post-Append projection
// writes. Deriving it from context.Background — not hc.Ctx, the daemon's shared
// root cancelled only on shutdown — lets the projection complete within
// cleanupTimeout even while hc.Ctx is being cancelled by daemon shutdown, so a
// durable question or answer is not abandoned mid-projection.
func cleanupCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cleanupTimeout)
}

// isBusy reports whether err is a transient SQLITE_BUSY/LOCKED contention error
// worth retrying. Detection goes through modernc.org/sqlite's *sqlite.Error and
// its Code(), so the narrow retry never swallows a constraint violation, a
// disk-full, or any other terminal error.
func isBusy(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	code := serr.Code()
	return code == sqliteBusy || code == sqliteLocked
}

// withBusyRetry runs a post-Append projection tx, retrying only on a transient
// SQLITE_BUSY/LOCKED. It does not retry constraint violations, disk-full,
// unknown-question, or an already-expired context — those surface immediately so
// the caller can fail closed.
func withBusyRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < busyRetries; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(busyBackoff):
		}
	}
	return err
}

const (
	notificationPending    = "pending"
	notificationDelivering = "delivering"
	notificationCompleted  = "completed"
)

type notificationDelivery struct {
	subjectID   string
	deliveryKey string
	eventSeq    int64
	payload     string
}

// claimNotificationDelivery durably enqueues a keyed notification and claims
// its pending delivery. A process crash leaves either pending or delivering;
// boot replay resets the latter before claiming it again.
func claimNotificationDelivery(ctx context.Context, db *sql.DB, subjectID, deliveryKey string, eventSeq int64, payload json.RawMessage) (bool, error) {
	if deliveryKey == "" {
		return true, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin notification delivery claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored string
	if err := tx.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE subject_id = ? AND seq = ?`, subjectID, eventSeq,
	).Scan(&stored); err != nil {
		return false, fmt.Errorf("read notification delivery event: %w", err)
	}
	if stored != string(payload) {
		return false, errors.New("notification delivery key reused with different payload")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO notification_deliveries(subject_id, delivery_key, event_seq, payload, state)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(subject_id, delivery_key) DO NOTHING`, subjectID, deliveryKey, eventSeq, payload, notificationPending); err != nil {
		return false, fmt.Errorf("enqueue notification delivery: %w", err)
	}
	var existingSeq int64
	var existingPayload string
	if err := tx.QueryRowContext(ctx, `
SELECT event_seq, payload FROM notification_deliveries
WHERE subject_id = ? AND delivery_key = ?`, subjectID, deliveryKey).Scan(&existingSeq, &existingPayload); err != nil {
		return false, fmt.Errorf("read notification delivery: %w", err)
	}
	if existingSeq != eventSeq || existingPayload != string(payload) {
		return false, errors.New("notification delivery key reused with different payload")
	}
	result, err := tx.ExecContext(ctx, `
UPDATE notification_deliveries SET state = ?
WHERE subject_id = ? AND delivery_key = ? AND state = ?`,
		notificationDelivering, subjectID, deliveryKey, notificationPending)
	if err != nil {
		return false, fmt.Errorf("claim notification delivery: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read notification delivery claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit notification delivery claim: %w", err)
	}
	return claimed == 1, nil
}

func finishNotificationDelivery(ctx context.Context, db *sql.DB, subjectID, deliveryKey string, delivered bool) error {
	if deliveryKey == "" {
		return nil
	}
	next := notificationPending
	if delivered {
		next = notificationCompleted
	}
	_, err := db.ExecContext(ctx, `
UPDATE notification_deliveries SET state = ?
WHERE subject_id = ? AND delivery_key = ? AND state = ?`,
		next, subjectID, deliveryKey, notificationDelivering)
	if err != nil {
		return fmt.Errorf("finish notification delivery: %w", err)
	}
	return nil
}

func notificationDeliveryCompletion(db *sql.DB, subjectID, deliveryKey string) func(error) {
	if deliveryKey == "" {
		return nil
	}
	return func(deliveryErr error) {
		ctx, cancel := cleanupCtx()
		defer cancel()
		_ = finishNotificationDelivery(ctx, db, subjectID, deliveryKey, deliveryErr == nil)
	}
}

// ReplayNotificationDeliveries resets interrupted claims and replays every
// durable pending keyed notification. It runs during boot after delivery lanes
// are installed and before Ready is published.
func ReplayNotificationDeliveries(ctx context.Context, db *sql.DB, fanout Fanout) error {
	if _, err := db.ExecContext(ctx, `UPDATE notification_deliveries SET state = ? WHERE state = ?`, notificationPending, notificationDelivering); err != nil {
		return fmt.Errorf("reset interrupted notification deliveries: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
SELECT subject_id, delivery_key, event_seq, payload
FROM notification_deliveries WHERE state = ?
ORDER BY subject_id, event_seq`, notificationPending)
	if err != nil {
		return fmt.Errorf("list pending notification deliveries: %w", err)
	}
	var pending []notificationDelivery
	for rows.Next() {
		var delivery notificationDelivery
		if err := rows.Scan(&delivery.subjectID, &delivery.deliveryKey, &delivery.eventSeq, &delivery.payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan pending notification delivery: %w", err)
		}
		pending = append(pending, delivery)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close pending notification deliveries: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pending notification deliveries: %w", err)
	}
	for _, delivery := range pending {
		result, err := db.ExecContext(ctx, `
UPDATE notification_deliveries SET state = ?
WHERE subject_id = ? AND delivery_key = ? AND state = ?`,
			notificationDelivering, delivery.subjectID, delivery.deliveryKey, notificationPending)
		if err != nil {
			return fmt.Errorf("claim replayed notification delivery: %w", err)
		}
		claimed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read replayed notification claim: %w", err)
		}
		if claimed != 1 {
			continue
		}
		var notification NotificationPayload
		if err := json.Unmarshal([]byte(delivery.payload), &notification); err != nil {
			return fmt.Errorf("decode pending notification delivery: %w", err)
		}
		fanout.Notification(delivery.subjectID, delivery.eventSeq, notification,
			notificationDeliveryCompletion(db, delivery.subjectID, delivery.deliveryKey))
	}
	return nil
}

// insertPendingAndAwait projects a freshly-published question AND engages the
// gate in ONE transaction: INSERT the open row, then UPDATE the subject to
// awaiting. Atomicity is the gate invariant — a concurrent handleAnswer's
// recordAnswer (also one tx) can only interleave at a tx boundary, never between
// these two statements, so it never observes an open row with the subject still
// idle nor idles a subject that has this open row. awaiting is unconditionally
// correct here because the very same tx just inserted an unanswered row.
// A duplicate row (composite PK) or any error rolls the whole tx back, leaving
// both the row and the status untouched.
func insertPendingAndAwait(ctx context.Context, db *sql.DB, subjectID string, questionID int64, header, payload string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ask tx: %w", err)
	}
	defer tx.Rollback()

	now := unix(time.Now())
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pending_questions(subject_id, question_id, header, payload, created_at) VALUES(?,?,?,?,?)`,
		subjectID, questionID, header, payload, now); err != nil {
		return fmt.Errorf("insert pending question: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE subjects SET status=?, updated_at=? WHERE id=?`,
		StatusAwaiting, now, subjectID); err != nil {
		return fmt.Errorf("await subject: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ask tx: %w", err)
	}
	return nil
}

// setSubjectStatus flips a subject's lifecycle status. handleAsk uses it only as
// the fail-CLOSED fallback when insertPendingAndAwait fails: the question is
// already durable, so the gate must engage even though the open row never landed.
func setSubjectStatus(ctx context.Context, db *sql.DB, subjectID, status string) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE subjects SET status=?, updated_at=? WHERE id=?`,
		status, unix(time.Now()), subjectID); err != nil {
		return fmt.Errorf("set subject status %s: %w", status, err)
	}
	return nil
}

// questionState reports whether a (subject, question) pair is known to the
// projection and, if so, whether it is already answered. handleAnswer calls it
// before any durable Append so an unknown target never reaches the log and an
// already-answered target is an idempotent no-op.
func questionState(ctx context.Context, db *sql.DB, subjectID string, questionID int64) (known, answered bool, err error) {
	var ans int
	row := db.QueryRowContext(ctx,
		`SELECT answered FROM pending_questions WHERE subject_id=? AND question_id=?`,
		subjectID, questionID)
	switch err := row.Scan(&ans); {
	case errors.Is(err, sql.ErrNoRows):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("question state: %w", err)
	}
	return true, ans == 1, nil
}

// openCount returns a subject's open (unanswered) question count.
func openCount(ctx context.Context, db *sql.DB, subjectID string) (int, error) {
	var open int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pending_questions WHERE subject_id=? AND answered=0`, subjectID).Scan(&open); err != nil {
		return 0, fmt.Errorf("count open questions: %w", err)
	}
	return open, nil
}

// durableAnswer returns the answer projection payload for the event at seq —
// the answer actually durable in the log, which under a dedup-key race is the
// first writer's, not the caller's own.
func durableAnswer(ctx context.Context, db *sql.DB, subjectID string, seq int64) (string, error) {
	var wire string
	if err := db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE subject_id=? AND seq=?`, subjectID, seq).Scan(&wire); err != nil {
		return "", fmt.Errorf("read durable answer: %w", err)
	}
	var a AnswerPayload
	if err := json.Unmarshal([]byte(wire), &a); err != nil {
		return "", fmt.Errorf("parse durable answer: %w", err)
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// recordAnswer marks a question answered and, when it was the last open one,
// flips the subject back to idle — both in one transaction so a concurrent ask
// cannot interleave and leak the gate. The UPDATE carries AND answered=0 as the
// real idempotency guard: a retried or racing answer that finds the row already
// answered makes no write and idles only on the count. The returned bool reports
// whether the subject idled.
func recordAnswer(ctx context.Context, db *sql.DB, subjectID string, questionID int64, answer string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin answer tx: %w", err)
	}
	defer tx.Rollback()

	now := unix(time.Now())
	if _, err := tx.ExecContext(ctx,
		`UPDATE pending_questions SET answered=1, answer=? WHERE subject_id=? AND question_id=? AND answered=0`,
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
			StatusIdle, now, subjectID); err != nil {
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
	if err := db.QueryRowContext(ctx,
		`SELECT answered, answer FROM pending_questions WHERE subject_id=? AND question_id=?`,
		subjectID, questionID).Scan(&answered, &answer); err != nil {
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
	return queryListedSubjects(ctx, db, `AND s.scope = ?`, scope)
}

// listSessions returns every active subject across scopes with its
// open-question count, for the REST sessions listing — a web client holds no
// scope, so it sees them all.
func listSessions(ctx context.Context, db *sql.DB) ([]ListedSubject, error) {
	return queryListedSubjects(ctx, db, ``)
}

// queryListedSubjects is the shared active-subject listing behind the socket
// list op and the REST sessions route. scopeFilter is a constant SQL fragment
// (empty or `AND s.scope = ?`), never caller input.
func queryListedSubjects(ctx context.Context, db *sql.DB, scopeFilter string, scopeArgs ...any) ([]ListedSubject, error) {
	args := append([]any{StatusIdle, StatusAwaiting}, scopeArgs...)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
SELECT s.id, s.scope, s.status, COUNT(p.question_id)
FROM subjects s
LEFT JOIN pending_questions p ON p.subject_id = s.id AND p.answered = 0
WHERE s.status IN (?, ?) %s
GROUP BY s.id, s.scope, s.status
ORDER BY s.created_at DESC, s.rowid DESC`, scopeFilter),
		args...)
	if err != nil {
		return nil, fmt.Errorf("list subjects: %w", err)
	}
	defer rows.Close()
	out := []ListedSubject{}
	for rows.Next() {
		var ls ListedSubject
		if err := rows.Scan(&ls.SubjectID, &ls.Scope, &ls.Status, &ls.Pending); err != nil {
			return nil, fmt.Errorf("scan listed subject: %w", err)
		}
		out = append(out, ls)
	}
	return out, rows.Err()
}

// subjectExists reports whether a subject row exists, so the REST pending route
// can 404 an unknown id instead of answering it with an empty set.
func subjectExists(ctx context.Context, db *sql.DB, subjectID string) (bool, error) {
	var one int
	switch err := db.QueryRowContext(ctx,
		`SELECT 1 FROM subjects WHERE id=?`, subjectID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("subject exists: %w", err)
	}
	return true, nil
}
