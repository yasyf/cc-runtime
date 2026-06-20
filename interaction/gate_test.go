package interaction

import (
	"context"
	"testing"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/subject"
)

func TestGateVerdict(t *testing.T) {
	gate := Gate()
	cases := []struct {
		name       string
		status     string
		wantAllow  bool
		wantReason string
	}{
		{name: "awaiting blocks", status: StatusAwaiting, wantAllow: false, wantReason: gateBlockReason},
		{name: "idle allows", status: StatusIdle, wantAllow: true, wantReason: ""},
		{name: "closed allows", status: StatusClosed, wantAllow: true, wantReason: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allow, reason := gate(context.Background(), subject.Subject{Status: tc.status}, daemon.ToolCall{})
			if allow != tc.wantAllow || reason != tc.wantReason {
				t.Fatalf("Gate(%q) = (%v, %q), want (%v, %q)", tc.status, allow, reason, tc.wantAllow, tc.wantReason)
			}
		})
	}
}

func TestGateErrorReasonIsBlockingMessage(t *testing.T) {
	if GateErrorReason == "" {
		t.Fatal("GateErrorReason must be a non-empty fail-closed message")
	}
}
