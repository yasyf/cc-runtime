package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
)

// runRPC executes the rpc command against the deps-resolved local daemon,
// returning its stdout and any error.
func runRPC(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c := rpcCmd(deps())
	// Mirror the production root, which silences cobra's usage/error printing so
	// only the raw reply lands on stdout.
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

// TestRPCAllowedOpRoundTrips drives an allowlisted op through the rpc
// passthrough against the real daemon harness and parses the raw reply.
func TestRPCAllowedOpRoundTrips(t *testing.T) {
	e := newE2E(t)
	subjectID := e.start()

	out, err := runRPC(t, "interaction.list", "--json", `{"scope":"`+e2eScope+`"}`)
	if err != nil {
		t.Fatalf("rpc interaction.list: %v\n%s", err, out)
	}
	var reply daemon.Reply
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &reply); err != nil {
		t.Fatalf("parse raw reply: %v\n%s", err, out)
	}
	if !reply.OK {
		t.Fatalf("reply not ok: %s", reply.Error)
	}
	var lr struct {
		Subjects []interaction.ListedSubject `json:"subjects"`
	}
	if err := json.Unmarshal(reply.Body, &lr); err != nil {
		t.Fatalf("parse list body: %v", err)
	}
	found := false
	for _, s := range lr.Subjects {
		if s.SubjectID == subjectID {
			found = true
		}
	}
	if !found {
		t.Fatalf("rpc list did not surface the started subject %q; got %+v", subjectID, lr.Subjects)
	}
}

// TestRPCDisallowedOpRefusedLocally asserts an off-allowlist op is refused
// before any socket dial: it errors with "not permitted" and writes no reply.
func TestRPCDisallowedOpRefusedLocally(t *testing.T) {
	for _, op := range []string{"guard-edit", "shutdown", "interaction.start"} {
		t.Run(op, func(t *testing.T) {
			out, err := runRPC(t, op)
			if err == nil {
				t.Fatalf("op %q was not refused", op)
			}
			if !strings.Contains(err.Error(), "not permitted") {
				t.Fatalf("op %q err = %v, want a not-permitted refusal", op, err)
			}
			if strings.TrimSpace(out) != "" {
				t.Fatalf("op %q wrote output despite refusal: %s", op, out)
			}
		})
	}
}

// TestRPCInvalidJSONRejected asserts malformed --json fails before any dial.
func TestRPCInvalidJSONRejected(t *testing.T) {
	out, err := runRPC(t, "interaction.list", "--json", "{not json")
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want an invalid-JSON error", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("invalid json wrote output: %s", out)
	}
}
