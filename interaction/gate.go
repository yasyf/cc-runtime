package interaction

import (
	"context"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/subject"
)

const gateBlockReason = "cc-runtime: the agent is awaiting your answer — edits are blocked until you respond (cc-runtime start)."

// GateErrorReason is the fail-closed message returned when a subject's status
// cannot be read; the guard blocks rather than silently permit.
const GateErrorReason = "cc-runtime: could not read interaction status; blocking the edit to be safe."

// Gate returns the edit-gate verdict: an awaiting subject blocks edits until the
// human answers; any other status permits them.
func Gate() daemon.GateFunc {
	return func(_ context.Context, s subject.Subject, _ daemon.ToolCall) (bool, string) {
		if s.Status == StatusAwaiting {
			return false, gateBlockReason
		}
		return true, ""
	}
}
