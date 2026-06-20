// Package interaction is cc-runtime's data+logic core: the daemon ops, event
// types, edit-gate, projection schema, and store helpers that back the
// harness-injected interaction tools (questions, notifications, answers). The
// subject Status is the gate signal — idle permits edits, awaiting blocks them
// until every open question is answered.
package interaction

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/paths"
	"github.com/yasyf/cc-interact/subject"
)

// AppName labels logs and user-facing daemon messages.
const AppName = "cc-runtime"

// appDir is the state-directory basename under the user's home.
const appDir = ".cc-runtime"

// AppPaths is the single source of truth for cc-runtime's state-directory
// layout: the socket, db, and http handshake the daemon and every client share.
func AppPaths() paths.Paths { return paths.Paths{App: appDir} }

// Domain ops the daemon routes to the interaction handlers.
const (
	OpStart               daemon.Op = "interaction.start"
	OpAsk                 daemon.Op = "interaction.ask"
	OpNotify              daemon.Op = "interaction.notify"
	OpAnswer              daemon.Op = "interaction.answer"
	OpAnswerPoll          daemon.Op = "interaction.answer-poll"
	OpPending             daemon.Op = "interaction.pending"
	OpList                daemon.Op = "interaction.list"
	OpCaptureNotification daemon.Op = "interaction.capture-notification"
)

// Event types appended to a subject's log.
const (
	EventQuestion     = "interaction.question"
	EventAnswer       = "interaction.answer"
	EventNotification = "interaction.notification"
)

// Subject lifecycle statuses. The status is the edit-gate signal.
const (
	StatusIdle     = "idle"
	StatusAwaiting = "awaiting"
	StatusClosed   = "closed"
)

// ActiveStatuses is the adoptable status set handed to the daemon.
var ActiveStatuses = []string{StatusIdle, StatusAwaiting}

// Lifecycle names the statuses the subject resolver writes on create and close.
var Lifecycle = subject.Lifecycle{Initial: StatusIdle, Closed: StatusClosed}

// slugFor is a subject's stable, printable name: deterministic per (session,
// scope) so a repeated start resumes the same subject, yet distinct sessions in
// one scope get distinct slugs — the subjects.slug unique index forbids a clash.
func slugFor(scope, session string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + scope))
	return "interaction-" + hex.EncodeToString(sum[:4])
}
