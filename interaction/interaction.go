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

// WrapSentinel is the unique marker `cc-runtime wrap` embeds in the child's
// appended system prompt and mirrors into the CC_RUNTIME_WRAP env var. The MCP
// channel reads the env var to detect a wrap launch: when set, the native
// AskUserQuestion/PushNotification are already disabled and the agent already
// steered, so the channel skips its soft-steer. Versioned so a steer change that
// alters the marker can't false-positive against a stale signal.
const WrapSentinel = "cc-runtime-wrap:v1"

// WrapEnvVar carries the WrapSentinel into the child process environment. It is
// the detection signal of record: a real run proved Claude Code never persists
// --append-system-prompt content to the session .jsonl, so the channel detector
// reads this env var rather than scanning the transcript (see launchedViaWrap).
const WrapEnvVar = "CC_RUNTIME_WRAP"

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
	OpCaptureQuestion     daemon.Op = "interaction.capture-question"
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

// ActiveStatuses is the adoptable status set handed to the daemon: only idle
// subjects are adopted across windows. An awaiting subject deliberately stays
// bound to its owning window — its owner recovers it via session-rotation rebind
// (keyed on session/pid, not adoption), it remains answerable by
// subject_id+question_id, and it stays visible and answerable in the TUI — so a
// fresh window in the same scope starts editable instead of inheriting a dead
// window's stale open-question edit-block.
//
// Accepted edge (pre-existing cc-interact behavior this set does NOT cover): the
// resolver's own-window pid-latest rebind is status-unfiltered, so under OS PID
// REUSE within one scope a new window that happens to reuse a dead window's pid
// can re-adopt that dead window's awaiting subject. ActiveStatuses gates the
// cross-window ADOPTION path, not this own-window rebind path, so it does not
// close this edge; it lives in cc-interact's resolver.
var ActiveStatuses = []string{StatusIdle}

// Lifecycle names the statuses the subject resolver writes on create and close.
var Lifecycle = subject.Lifecycle{Initial: StatusIdle, Closed: StatusClosed}

// slugFor is a subject's stable, printable name: deterministic per (session,
// scope) so a repeated start resumes the same subject, yet distinct sessions in
// one scope get distinct slugs — the subjects.slug unique index forbids a clash.
func slugFor(scope, session string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + scope))
	return "interaction-" + hex.EncodeToString(sum[:4])
}
