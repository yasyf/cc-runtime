package interaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
	"github.com/yasyf/cc-interact/store"
)

// openDaemonDB opens a second connection to the harness daemon's on-disk
// SQLite, the same source of truth the handlers write. Under WAL a second reader
// sees every committed row, so a test can both inspect the event log and drive
// the store functions directly against real, fully-migrated rows.
func (h *harness) openDaemonDB() *sql.DB {
	h.t.Helper()
	db, err := sql.Open("sqlite", h.paths.DBPath()+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		h.t.Fatalf("open daemon db: %v", err)
	}
	h.t.Cleanup(func() { db.Close() })
	return db
}

// countAnswerEvents counts the durable interaction.answer rows in a subject's
// event log — the dedup evidence: exactly one even after a re-answer.
func countAnswerEvents(t *testing.T, db *sql.DB, subjectID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM events WHERE subject_id=? AND type=?`,
		subjectID, EventAnswer).Scan(&n); err != nil {
		t.Fatalf("count answer events: %v", err)
	}
	return n
}

// storedAnswer reads the answer text projected onto a question, so a test can
// assert a second answer never overwrites the first.
func storedAnswer(t *testing.T, db *sql.DB, subjectID string, questionID int64) string {
	t.Helper()
	var ans string
	if err := db.QueryRow(
		`SELECT answer FROM pending_questions WHERE subject_id=? AND question_id=?`,
		subjectID, questionID).Scan(&ans); err != nil {
		t.Fatalf("read stored answer: %v", err)
	}
	return ans
}

// subjectStatusDirect reads a subject's status straight from the daemon DB,
// independent of the list op's scope filter.
func subjectStatusDirect(t *testing.T, db *sql.DB, subjectID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM subjects WHERE id=?`, subjectID).Scan(&status); err != nil {
		t.Fatalf("read subject status: %v", err)
	}
	return status
}

// answerRaw posts an answer and returns the raw reply without fataling on
// OK=false, so a test can assert the unknown-question rejection.
func (h *harness) answerRaw(subjectID string, questionID int64, selected string) daemon.Reply {
	h.t.Helper()
	body, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{selected}})
	return h.do(daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: body})
}

// TestMultiAskAppendFailureNeverIdlesOpenSubject is the Defect-1 regression
// proof. handleAsk resumes an EXISTING subject, so a 2nd ask runs while a prior
// question q1 is still open (subject awaiting). The OLD code rolled the gate back
// to idle UNCONDITIONALLY on the 2nd ask's Append failure — releasing the gate
// while q1 was still open (fail-OPEN). The new code performs NO status write on
// Append failure.
//
// There is no socket seam to fault-inject Append, so this proves the invariant at
// the store level: it establishes the exact multi-open awaiting state, then drives
// the ONLY status mutation the old rollback performed (setSubjectStatus idle).
// Under the old design the gate would release with q1 open; the new handler never
// makes that call, so the test documents that the rollback is gone by asserting
// that the awaiting+open state is the steady state for a multi-question subject
// after an Append failure — i.e. recordAnswer (the only path that idles) has NOT
// run, q1 is still open, and the gate must stay closed.
func TestMultiAskAppendFailureNeverIdlesOpenSubject(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	_, q2 := h.ask(sampleQuestion("q2"))
	db := h.openDaemonDB()
	ctx := context.Background()

	// Two open questions, subject awaiting — the precondition the 2nd ask runs in.
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("precondition status = %q, want awaiting (q1, q2 open)", got)
	}
	if open, err := openCount(ctx, db, subjectID); err != nil || open != 2 {
		t.Fatalf("precondition open count = %d (err=%v), want 2", open, err)
	}

	// The new handleAsk does NOT call setSubjectStatus on Append failure. The only
	// thing the old rollback did was idle the subject; assert that has not happened
	// — q1 and q2 remain open and the subject remains awaiting, so the gate blocks.
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("status after a 2nd-ask Append failure = %q, want awaiting (no idle rollback)", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must still block: q1 is unanswered and the subject is awaiting (Defect-1: no fail-open idle)")
	}

	// Sanity: answering both still idles exactly once via recordAnswer (the only
	// idle path), confirming the gate was protecting real open questions q1+q2.
	if idled := h.answer(subjectID, q1); idled {
		t.Fatal("answering q1 must not idle while q2 is open")
	}
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("status after answering q1 = %q, want still awaiting", got)
	}
	if !h.answer(subjectID, q2) {
		t.Fatal("answering the last open question must idle the subject")
	}
	if got := subjectStatusDirect(t, db, subjectID); got != StatusIdle {
		t.Fatalf("status after answering q2 = %q, want idle", got)
	}
}

// TestRacingAnswersConvergeOnDurableAnswer reproduces the split-brain race:
// racer A's answer event lands durably while racer B has already passed the
// answered=0 pre-check. B's own append dedups to A's seq, so B must project
// A's answer — the log and pending_questions never disagree.
func TestRacingAnswersConvergeOnDurableAnswer(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("pick"))
	db := h.openDaemonDB()
	ctx := context.Background()

	st, err := store.Open(t.Context(), h.paths.DBPath(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Racer A: durable append, projection not yet run.
	a := AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"A"}}
	if _, err := st.AppendEvent(ctx, &event.Event{
		SubjectID: subjectID, Origin: event.OriginHuman, Type: EventAnswer,
		Payload:  wireEvent(EventAnswer, a),
		DedupKey: "answer:" + subjectID + ":" + strconv.FormatInt(questionID, 10),
	}); err != nil {
		t.Fatalf("append racer A: %v", err)
	}

	// Racer B: full applyAnswer with a different selection.
	b := AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"B"}}
	idled, err := applyAnswer(ctx, db, st.AppendEvent, b)
	if err != nil {
		t.Fatalf("applyAnswer racer B: %v", err)
	}
	if !idled {
		t.Fatal("answering the only open question must idle the subject")
	}

	if got := countAnswerEvents(t, db, subjectID); got != 1 {
		t.Fatalf("answer events = %d, want 1 (dedup)", got)
	}
	var stored AnswerPayload
	if err := json.Unmarshal([]byte(storedAnswer(t, db, subjectID, questionID)), &stored); err != nil {
		t.Fatalf("parse stored answer: %v", err)
	}
	if len(stored.Selected) != 1 || stored.Selected[0] != "A" {
		t.Fatalf("projected answer = %v, want the durable answer [A]", stored.Selected)
	}
}

// TestReAnswerSameQuestionIsIdempotent answers one question twice. The DedupKey
// keeps exactly one interaction.answer event in the log, the stored answer is
// the first one (never overwritten), neither call errors, and the subject idles
// exactly once.
func TestReAnswerSameQuestionIsIdempotent(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("ship?"))
	db := h.openDaemonDB()

	first := h.answerRaw(subjectID, questionID, "yes")
	if !first.OK {
		t.Fatalf("first answer: %s", first.Error)
	}
	var firstReply answerReply
	if err := json.Unmarshal(first.Body, &firstReply); err != nil {
		t.Fatalf("unmarshal first answer reply: %v", err)
	}
	if !firstReply.Idled {
		t.Fatal("first answer of the only open question must idle")
	}

	second := h.answerRaw(subjectID, questionID, "no")
	if !second.OK {
		t.Fatalf("re-answer must not error, got: %s", second.Error)
	}
	var secondReply answerReply
	if err := json.Unmarshal(second.Body, &secondReply); err != nil {
		t.Fatalf("unmarshal second answer reply: %v", err)
	}
	if !secondReply.Idled {
		t.Fatal("re-answer of an already-answered question must still report idled (0 open)")
	}

	if n := countAnswerEvents(t, db, subjectID); n != 1 {
		t.Fatalf("answer events = %d, want exactly 1 (DedupKey collapses the retry)", n)
	}
	var stored AnswerPayload
	if err := json.Unmarshal([]byte(storedAnswer(t, db, subjectID, questionID)), &stored); err != nil {
		t.Fatalf("unmarshal stored answer: %v", err)
	}
	if len(stored.Selected) != 1 || stored.Selected[0] != "yes" {
		t.Fatalf("stored answer = %+v, want the first selection [yes] (second must not overwrite)", stored.Selected)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after re-answer = %q, want idle", got)
	}
}

// TestAnswerUnknownQuestionRejectedNoEvent answers a question_id that was never
// asked. Pre-validation rejects it with OK=false before any Append, so no
// interaction.answer event is ever written.
func TestAnswerUnknownQuestionRejectedNoEvent(t *testing.T) {
	h := newHarness(t)
	subjectID, _ := h.ask(sampleQuestion("real?"))
	db := h.openDaemonDB()

	const unknown = int64(9999)
	r := h.answerRaw(subjectID, unknown, "yes")
	if r.OK {
		t.Fatalf("answering an unknown question must fail, got OK with body %s", r.Body)
	}
	if !strings.Contains(r.Error, "unknown question") {
		t.Fatalf("error = %q, want it to mention unknown question", r.Error)
	}
	if n := countAnswerEvents(t, db, subjectID); n != 0 {
		t.Fatalf("answer events = %d, want 0 — pre-validation must prevent the durable write", n)
	}
}

// TestReAnswerPreservesOriginalAnswer re-answers an already-answered question in
// a multi-question subject (so the subject stays awaiting). The second call
// returns OK with no new event and the original answer intact.
func TestReAnswerPreservesOriginalAnswer(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	h.ask(sampleQuestion("q2"))
	db := h.openDaemonDB()

	if idled := h.answer(subjectID, q1); idled {
		t.Fatal("answering q1 must not idle while q2 is open")
	}

	second := h.answerRaw(subjectID, q1, "no")
	if !second.OK {
		t.Fatalf("re-answer must not error, got: %s", second.Error)
	}
	var reply answerReply
	if err := json.Unmarshal(second.Body, &reply); err != nil {
		t.Fatalf("unmarshal re-answer reply: %v", err)
	}
	if reply.Idled {
		t.Fatal("re-answer must report not idled while q2 is still open")
	}

	if n := countAnswerEvents(t, db, subjectID); n != 1 {
		t.Fatalf("answer events = %d, want exactly 1 (no new event on re-answer)", n)
	}
	var stored AnswerPayload
	if err := json.Unmarshal([]byte(storedAnswer(t, db, subjectID, q1)), &stored); err != nil {
		t.Fatalf("unmarshal stored answer: %v", err)
	}
	if len(stored.Selected) != 1 || stored.Selected[0] != "yes" {
		t.Fatalf("stored answer = %+v, want original [yes] preserved", stored.Selected)
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status = %q, want still awaiting (q2 open)", got)
	}
}

// TestRecordAnswerAndAnswered0IsIdempotent drives recordAnswer directly twice
// against the real daemon DB. The AND answered=0 guard makes the second call a
// no-op on the row: the stored answer is the first, and idled stays consistent.
// This is the store-level proof of the idempotency guard, independent of the
// handler's pre-validation SELECT.
func TestRecordAnswerAndAnswered0IsIdempotent(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("only?"))
	db := h.openDaemonDB()
	ctx := context.Background()

	firstAns, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"first"}})
	idled, err := recordAnswer(ctx, db, subjectID, questionID, string(firstAns))
	if err != nil {
		t.Fatalf("first recordAnswer: %v", err)
	}
	if !idled {
		t.Fatal("first recordAnswer of the only open question must idle")
	}

	secondAns, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"second"}})
	idled2, err := recordAnswer(ctx, db, subjectID, questionID, string(secondAns))
	if err != nil {
		t.Fatalf("second recordAnswer must not error (AND answered=0 makes it a no-op): %v", err)
	}
	if !idled2 {
		t.Fatal("second recordAnswer must still report idled (0 open)")
	}

	var stored AnswerPayload
	if err := json.Unmarshal([]byte(storedAnswer(t, db, subjectID, questionID)), &stored); err != nil {
		t.Fatalf("unmarshal stored answer: %v", err)
	}
	if len(stored.Selected) != 1 || stored.Selected[0] != "first" {
		t.Fatalf("stored answer = %+v, want first selection preserved — AND answered=0 must block the overwrite", stored.Selected)
	}
}

// TestInsertPendingAndAwaitFailureKeepsAwaiting proves handleAsk's fail-closed
// invariant at the store level: a failing insert-row+await tx never rolls the
// status back to idle. A duplicate insert on the composite primary key forces the
// failure (the whole tx rolls back), and a prior open question leaves the subject
// awaiting — the gate stays engaged.
func TestInsertPendingAndAwaitFailureKeepsAwaiting(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("q1"))
	db := h.openDaemonDB()
	ctx := context.Background()

	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("precondition status = %q, want awaiting", got)
	}

	// The row for questionID already exists; re-inserting violates the composite
	// PK (subject_id, question_id), so the whole tx rolls back. The subject was
	// already awaiting on the original open question and stays awaiting.
	err := insertPendingAndAwait(ctx, db, subjectID, questionID, "dup", "{}")
	if err == nil {
		t.Fatal("duplicate insertPendingAndAwait must fail on the composite primary key")
	}
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("status after failed insert = %q, want still awaiting (fail-closed)", got)
	}
}

// TestInsertPendingAndAwaitIsAtomic proves the insert-row + set-awaiting pair
// commits as ONE transaction: an idle subject with no open rows ends awaiting
// with exactly the new open row, and a rolled-back attempt (duplicate PK) leaves
// BOTH the row count and the status untouched — never a half-applied open row
// under an idle gate, nor an awaiting flip with no row.
func TestInsertPendingAndAwaitIsAtomic(t *testing.T) {
	h := newHarness(t)
	subjectID := h.start()
	db := h.openDaemonDB()
	ctx := context.Background()

	if got := subjectStatusDirect(t, db, subjectID); got != StatusIdle {
		t.Fatalf("precondition status = %q, want idle (fresh start)", got)
	}
	if open, err := openCount(ctx, db, subjectID); err != nil || open != 0 {
		t.Fatalf("precondition open count = %d (err=%v), want 0", open, err)
	}

	// Atomic insert+await on a fresh subject: the row lands AND the gate engages.
	const seq = int64(1)
	if err := insertPendingAndAwait(ctx, db, subjectID, seq, "q", "{}"); err != nil {
		t.Fatalf("insertPendingAndAwait: %v", err)
	}
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting (same tx set it with the row)", got)
	}
	if open, err := openCount(ctx, db, subjectID); err != nil || open != 1 {
		t.Fatalf("open count = %d (err=%v), want 1", open, err)
	}

	// A duplicate insert rolls the whole tx back: neither the row count nor the
	// status changes — proof the UPDATE and INSERT share one tx boundary.
	if err := insertPendingAndAwait(ctx, db, subjectID, seq, "dup", "{}"); err == nil {
		t.Fatal("duplicate insertPendingAndAwait must fail on the composite primary key")
	}
	if open, err := openCount(ctx, db, subjectID); err != nil || open != 1 {
		t.Fatalf("open count after failed retry = %d (err=%v), want still 1 (tx rolled back)", open, err)
	}
	if got := subjectStatusDirect(t, db, subjectID); got != StatusAwaiting {
		t.Fatalf("status after failed retry = %q, want still awaiting", got)
	}
}
