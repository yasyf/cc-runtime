package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
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

// TestRPCMeshPresenceRoundTrips drives `rpc mesh.presence` through the passthrough
// against the real daemon: the reply decodes into a Presence report — a live read
// on darwin, the documented degradation elsewhere — proving the op is allowlisted
// and the handler is wired.
func TestRPCMeshPresenceRoundTrips(t *testing.T) {
	newE2E(t)

	out, err := runRPC(t, string(mesh.OpPresence))
	if err != nil {
		t.Fatalf("rpc mesh.presence: %v\n%s", err, out)
	}
	var reply daemon.Reply
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &reply); err != nil {
		t.Fatalf("parse raw reply: %v\n%s", err, out)
	}
	if !reply.OK {
		t.Fatalf("reply not ok: %s", reply.Error)
	}
	var p mesh.Presence
	if err := json.Unmarshal(reply.Body, &p); err != nil {
		t.Fatalf("parse presence body: %v", err)
	}
	if !p.Attended && p.Reason == "" {
		t.Fatalf("unattended presence carries no reason: %+v", p)
	}
}

// TestRPCSessionKeysDistinctSubjects proves the passthrough carries the window
// identity: notifies under distinct --session values land on distinct subjects
// (the routed-surface collision fix), while a repeated session resumes its own.
func TestRPCSessionKeysDistinctSubjects(t *testing.T) {
	newE2E(t)

	notify := func(session string) string {
		t.Helper()
		out, err := runRPC(t, string(interaction.OpNotify),
			"--cwd", e2eScope, "--session", session, "--json", `{"message":"needs you"}`)
		if err != nil {
			t.Fatalf("rpc notify (%s): %v\n%s", session, err, out)
		}
		var reply daemon.Reply
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &reply); err != nil {
			t.Fatalf("parse raw reply: %v\n%s", err, out)
		}
		if !reply.OK {
			t.Fatalf("notify (%s) not ok: %s", session, reply.Error)
		}
		if reply.SubjectID == "" {
			t.Fatalf("notify (%s) reply carries no subject id", session)
		}
		return reply.SubjectID
	}

	a := notify("routed:origin-a:subj-1")
	b := notify("routed:origin-b:subj-1")
	if a == b {
		t.Fatalf("distinct origin sessions collided on subject %s", a)
	}
	if again := notify("routed:origin-a:subj-1"); again != a {
		t.Fatalf("repeated session resumed subject %s, want %s", again, a)
	}
}

// TestRPCJSONFromStdin asserts `--json -` reads the params off stdin — the mesh
// fan-out's leg, keeping payloads out of argv — and round-trips like a literal.
func TestRPCJSONFromStdin(t *testing.T) {
	e := newE2E(t)
	subjectID := e.start()

	var out bytes.Buffer
	c := rpcCmd(deps())
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(`{"scope":"` + e2eScope + `"}`))
	c.SetArgs([]string{"interaction.list", "--json", "-"})
	if err := c.Execute(); err != nil {
		t.Fatalf("rpc --json -: %v\n%s", err, out.String())
	}
	var reply daemon.Reply
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatalf("parse raw reply: %v\n%s", err, out.String())
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
		t.Fatalf("stdin-fed rpc list did not surface subject %q; got %+v", subjectID, lr.Subjects)
	}
}

// TestRPCInvalidStdinJSONRejected asserts malformed stdin params fail before any dial.
func TestRPCInvalidStdinJSONRejected(t *testing.T) {
	var out bytes.Buffer
	c := rpcCmd(deps())
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader("{not json"))
	c.SetArgs([]string{"interaction.list", "--json", "-"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want an invalid-JSON error", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("invalid stdin json wrote output: %s", out.String())
	}
}

// TestRPCOversizedStdinJSONRejected asserts stdin params over the cap fail before any dial.
func TestRPCOversizedStdinJSONRejected(t *testing.T) {
	var out bytes.Buffer
	c := rpcCmd(deps())
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(`"` + strings.Repeat("x", maxRPCStdinBytes-1) + `"`))
	c.SetArgs([]string{"interaction.list", "--json", "-"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "exceeds 1048576 bytes") {
		t.Fatalf("err = %v, want an over-cap stdin error", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("oversized stdin json wrote output: %s", out.String())
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
