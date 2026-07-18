package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

// runRPC executes the rpc command against the deps-resolved local daemon,
// feeding stdin (the only params channel) and returning its stdout and any error.
func runRPC(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c := rpcCmd(deps())
	// Mirror the production root, which silences cobra's usage/error printing so
	// only the raw reply lands on stdout.
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(stdin))
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

// TestRPCAllowedOpRoundTrips drives an allowlisted op through the rpc
// passthrough against the real daemon harness and parses the raw reply.
func TestRPCAllowedOpRoundTrips(t *testing.T) {
	e := newE2E(t)
	subjectID := e.start()

	out, err := runRPC(t, `{"scope":"`+e2eScope+`"}`, "interaction.list", "--json", "-")
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

	out, err := runRPC(t, "", string(mesh.OpPresence))
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
		out, err := runRPC(t, `{"message":"needs you"}`, string(interaction.OpNotify),
			"--cwd", e2eScope, "--session", session, "--json", "-")
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
// fan-out's leg, keeping payloads out of argv — and round-trips.
func TestRPCJSONFromStdin(t *testing.T) {
	e := newE2E(t)
	subjectID := e.start()

	out, err := runRPC(t, `{"scope":"`+e2eScope+`"}`, "interaction.list", "--json", "-")
	if err != nil {
		t.Fatalf("rpc --json -: %v\n%s", err, out)
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
		t.Fatalf("stdin-fed rpc list did not surface subject %q; got %+v", subjectID, lr.Subjects)
	}
}

// TestRPCInvalidStdinJSONRejected asserts malformed stdin params fail before any
// dial with the exact length-only error — the payload can carry free-text answer
// content and the error rides over ssh into logs.
func TestRPCInvalidStdinJSONRejected(t *testing.T) {
	const payload = `{not json, secret-answer-note`
	out, err := runRPC(t, payload, "interaction.list", "--json", "-")
	want := fmt.Sprintf("--json is not valid JSON (%d bytes)", len(payload))
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if strings.Contains(out, "secret-answer-note") || strings.TrimSpace(out) != "" {
		t.Fatalf("invalid stdin json wrote output: %s", out)
	}
}

// TestRPCOversizedStdinJSONRejected asserts stdin params over the cap fail before
// any dial with the exact length-only error.
func TestRPCOversizedStdinJSONRejected(t *testing.T) {
	out, err := runRPC(t, `"`+strings.Repeat("x", maxRPCStdinBytes-1)+`"`,
		"interaction.list", "--json", "-")
	const want = "--json from stdin exceeds 1048576 bytes"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("oversized stdin json wrote output: %s", out)
	}
}

// TestRPCDisallowedOpRefusedLocally asserts an off-allowlist op is refused
// before any socket dial: it errors with "not permitted" and writes no reply.
func TestRPCDisallowedOpRefusedLocally(t *testing.T) {
	for _, op := range []string{"guard-edit", "shutdown", "interaction.start"} {
		t.Run(op, func(t *testing.T) {
			out, err := runRPC(t, "", op)
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

// TestRPCLiteralJSONArgvRejected asserts the literal `--json <json>` argv form is
// gone: a non-"-" value anywhere on argv is refused before any dial — even valid
// JSON, and even when a repeated `--json -` would win a last-one-wins parse —
// with the exact refusal, which never echoes the ps-visible value.
func TestRPCLiteralJSONArgvRejected(t *testing.T) {
	for _, tc := range []struct {
		id   string
		args []string
	}{
		{"valid json", []string{"--json", `{"notes":"secret-answer-note"}`}},
		{"invalid json", []string{"--json", `{not json, secret-answer-note`}},
		{"literal laundered by trailing stdin flag", []string{"--json", `{"notes":"secret-answer-note"}`, "--json", "-"}},
		{"literal after stdin flag", []string{"--json", "-", "--json", `{"notes":"secret-answer-note"}`}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			out, err := runRPC(t, "", append([]string{"interaction.list"}, tc.args...)...)
			const want = `--json accepts only "-": params ride stdin, never argv`
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want exactly %q", err, want)
			}
			if strings.Contains(err.Error(), "secret-answer-note") {
				t.Fatalf("refusal echoes the argv payload: %v", err)
			}
			if strings.TrimSpace(out) != "" {
				t.Fatalf("literal --json wrote output: %s", out)
			}
		})
	}
}
