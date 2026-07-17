package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
)

const (
	host1     = "bob@srv.tail.ts.net"
	host2     = "carol@lap.tail.ts.net"
	testSelf  = "alice@mac.tail.ts.net"
	opsScope  = "/repo"
	opsSubjID = "subj-7"
)

// localKey is the exact "name args" key MockRunner.Local scripts on for a local
// `cc-runtime rpc <op> --json -` fan-out call; the params ride stdin.
func localKey(op daemon.Op) string {
	return Binary + " rpc " + string(op) + " --json -"
}

// replyLine builds the single daemon.Reply JSON line the rpc passthrough prints.
func replyLine(t *testing.T, ok bool, errMsg string, body any) string {
	t.Helper()
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	b, err := json.Marshal(daemon.Reply{OK: ok, Error: errMsg, Body: raw})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	return string(b)
}

func listReply(t *testing.T, port int, subjects ...interaction.ListedSubject) string {
	t.Helper()
	return replyLine(t, true, "", listBody{Subjects: subjects, HTTPPort: port})
}

func pendingReply(t *testing.T, questions ...interaction.PendingQuestion) string {
	t.Helper()
	return replyLine(t, true, "", pendingBody{Questions: questions})
}

// dialMap hands each host its own MockRunner, failing on an unregistered host.
func dialMap(t *testing.T, m map[string]*MockRunner) func(string) Runner {
	t.Helper()
	return func(target string) Runner {
		r, ok := m[target]
		if !ok {
			t.Fatalf("dial for unregistered host %q", target)
		}
		return r
	}
}

func TestListAllMergesLocalAndHostsInOrder(t *testing.T) {
	reg := &hostregistry.Registry{Self: testSelf, Hosts: []string{host1, host2}}

	local := NewMockRunner().OnLocal(localKey(interaction.OpList),
		listReply(t, 4001, interaction.ListedSubject{SubjectID: "local-a", Status: interaction.StatusAwaiting, Pending: 1}), nil)
	h1 := NewMockRunner().OnSSH("interaction.list",
		listReply(t, 4002, interaction.ListedSubject{SubjectID: "h1-a", Status: interaction.StatusIdle}), nil)
	// host2 is unreachable: ssh fails with no stdout.
	h2 := NewMockRunner().OnSSH("interaction.list", "", errors.New("ssh: connect to host lap port 22: Connection refused"))

	results, err := ListAll(context.Background(), reg, opsScope, local, dialMap(t, map[string]*MockRunner{host1: h1, host2: h2}))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3 (self + 2 hosts)", len(results))
	}

	// Merge order is local first, then hosts in registry order.
	if !results[0].Local || results[0].Host != testSelf {
		t.Fatalf("results[0] = %+v, want the local machine first", results[0])
	}
	if results[0].Node() != "local" {
		t.Fatalf("local Node() = %q, want %q", results[0].Node(), "local")
	}
	if len(results[0].Subjects) != 1 || results[0].Subjects[0].SubjectID != "local-a" {
		t.Fatalf("local subjects = %+v, want [local-a]", results[0].Subjects)
	}
	if results[0].HTTPPort != 4001 {
		t.Fatalf("local HTTPPort = %d, want 4001", results[0].HTTPPort)
	}

	if results[1].Host != host1 || results[1].Local {
		t.Fatalf("results[1] = %+v, want host1 (remote)", results[1])
	}
	if results[1].Node() != "srv" {
		t.Fatalf("host1 Node() = %q, want %q", results[1].Node(), "srv")
	}
	if len(results[1].Subjects) != 1 || results[1].Subjects[0].SubjectID != "h1-a" {
		t.Fatalf("host1 subjects = %+v, want [h1-a]", results[1].Subjects)
	}

	// The dead host is a per-host error, not a whole-fanout failure.
	if results[2].Host != host2 {
		t.Fatalf("results[2].Host = %q, want %q", results[2].Host, host2)
	}
	if results[2].Err == nil {
		t.Fatal("unreachable host2 must surface a per-host error")
	}
	if results[2].Subjects != nil {
		t.Fatalf("unreachable host2 must carry no subjects, got %+v", results[2].Subjects)
	}

	// host1's rpc went to host1's ssh target, carried the op on argv and the
	// scope only on stdin.
	calls := h1.SSHCalls(host1)
	if len(calls) != 1 {
		t.Fatalf("host1 ssh calls = %d, want exactly 1 against %q; all = %v", len(calls), host1, h1.SSHCmdsAll())
	}
	if !strings.Contains(calls[0].Cmd, "interaction.list") || !strings.HasSuffix(calls[0].Cmd, "--json -") {
		t.Fatalf("list cmd = %q, want the op and a stdin-fed --json -", calls[0].Cmd)
	}
	var sent listParams
	if err := json.Unmarshal([]byte(calls[0].Stdin), &sent); err != nil || sent.Scope != opsScope {
		t.Fatalf("list stdin = %q (err %v), want the scope params", calls[0].Stdin, err)
	}
	if strings.Contains(calls[0].Cmd, opsScope) {
		t.Fatalf("list cmd %q leaked the params onto argv", calls[0].Cmd)
	}
	// The local leg fed its params over stdin too.
	locals := local.LocalCalls()
	if len(locals) != 1 {
		t.Fatalf("local calls = %d, want 1", len(locals))
	}
	if err := json.Unmarshal([]byte(locals[0].Stdin), &sent); err != nil || sent.Scope != opsScope {
		t.Fatalf("local stdin = %q (err %v), want the scope params", locals[0].Stdin, err)
	}
}

func TestListAllDaemonErrorReplyIsPerHost(t *testing.T) {
	reg := &hostregistry.Registry{Self: testSelf, Hosts: []string{host1}}
	local := NewMockRunner().OnLocal(localKey(interaction.OpList), listReply(t, 4001), nil)
	// A well-formed error reply (passthrough exits nonzero) is a benign per-host
	// error, never a malformed-reply fail-loud.
	h1 := NewMockRunner().OnSSH("interaction.list",
		replyLine(t, false, "scope is required", nil), errors.New("exit status 1"))

	results, err := ListAll(context.Background(), reg, opsScope, local, dialMap(t, map[string]*MockRunner{host1: h1}))
	if err != nil {
		t.Fatalf("a well-formed error reply must not fail the whole fan-out: %v", err)
	}
	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), "scope is required") {
		t.Fatalf("host1 err = %v, want the daemon's error message surfaced per-host", results[1].Err)
	}
}

func TestListAllMalformedReplyFailsLoud(t *testing.T) {
	reg := &hostregistry.Registry{Self: testSelf, Hosts: []string{host1}}
	local := NewMockRunner().OnLocal(localKey(interaction.OpList), listReply(t, 4001), nil)

	t.Run("non-reply output", func(t *testing.T) {
		h1 := NewMockRunner().OnSSH("interaction.list", "not a json reply\n", nil)
		_, err := ListAll(context.Background(), reg, opsScope, local, dialMap(t, map[string]*MockRunner{host1: h1}))
		if err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("err = %v, want a loud malformed-reply failure", err)
		}
		if !strings.Contains(err.Error(), "srv") {
			t.Fatalf("err = %v, want the offending host node named", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		h1 := NewMockRunner().OnSSH("interaction.list", `{"ok":true,"body":{"subjects":"not-an-array"}}`, nil)
		_, err := ListAll(context.Background(), reg, opsScope, local, dialMap(t, map[string]*MockRunner{host1: h1}))
		if err == nil || !strings.Contains(err.Error(), "decode interaction.list") {
			t.Fatalf("err = %v, want a loud body-decode failure", err)
		}
	})
}

func TestPendingAllReturnsOwningHostQuestions(t *testing.T) {
	reg := &hostregistry.Registry{Self: testSelf, Hosts: []string{host1}}
	// The subject lives on host1; the local machine has no open questions for it.
	local := NewMockRunner().OnLocal(localKey(interaction.OpPending), pendingReply(t), nil)
	h1 := NewMockRunner().OnSSH("interaction.pending",
		pendingReply(t, interaction.PendingQuestion{QuestionID: 12, Payload: `{"prompt":"which?"}`}), nil)

	results, err := PendingAll(context.Background(), reg, opsSubjID, local, dialMap(t, map[string]*MockRunner{host1: h1}))
	if err != nil {
		t.Fatalf("PendingAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if len(results[0].Questions) != 0 {
		t.Fatalf("local questions = %+v, want none", results[0].Questions)
	}
	if len(results[1].Questions) != 1 || results[1].Questions[0].QuestionID != 12 {
		t.Fatalf("host1 questions = %+v, want the single open question 12", results[1].Questions)
	}
	calls := h1.SSHCalls(host1)
	if len(calls) != 1 {
		t.Fatalf("pending rpc calls against %q = %d, want 1; all = %v", host1, len(calls), h1.SSHCmdsAll())
	}
	var sent pendingParams
	if err := json.Unmarshal([]byte(calls[0].Stdin), &sent); err != nil || sent.SubjectID != opsSubjID {
		t.Fatalf("pending stdin = %q (err %v), want the subject id params", calls[0].Stdin, err)
	}
	if strings.Contains(calls[0].Cmd, opsSubjID) {
		t.Fatalf("pending cmd %q leaked the subject id onto argv", calls[0].Cmd)
	}
}

func TestPendingAllDeadHostIsPerHost(t *testing.T) {
	reg := &hostregistry.Registry{Self: testSelf, Hosts: []string{host1}}
	local := NewMockRunner().OnLocal(localKey(interaction.OpPending), pendingReply(t), nil)
	h1 := NewMockRunner().OnSSH("interaction.pending", "", errors.New("Connection refused"))

	results, err := PendingAll(context.Background(), reg, opsSubjID, local, dialMap(t, map[string]*MockRunner{host1: h1}))
	if err != nil {
		t.Fatalf("a dead host must not fail PendingAll: %v", err)
	}
	if results[1].Err == nil {
		t.Fatal("dead host must surface a per-host error")
	}
}

func TestAnswerRemoteSubmitsAndReportsRelease(t *testing.T) {
	runner := NewMockRunner().OnSSH("interaction.answer", replyLine(t, true, "", answerBody{Idled: true}), nil)
	a := interaction.AnswerPayload{SubjectID: opsSubjID, QuestionID: 9, Selected: []string{"Rewrite"}, Notes: "go"}

	idled, err := AnswerRemote(context.Background(), runner, host1, a)
	if err != nil {
		t.Fatalf("AnswerRemote: %v", err)
	}
	if !idled {
		t.Fatal("AnswerRemote must report the subject idled (gate released)")
	}

	calls := runner.SSHCalls(host1)
	if len(calls) != 1 {
		t.Fatalf("ssh calls against %q = %d, want 1", host1, len(calls))
	}
	call := calls[0]
	// The daemon dedupes repeated answers, so the failover leg is the right one:
	// an at-least-once retry can only improve delivery.
	if call.Kind != "ssh" {
		t.Fatalf("answer kind = %q, want the failover ssh leg", call.Kind)
	}
	if call.Cmd != Binary+" rpc "+hostregistry.ShellQuote(string(interaction.OpAnswer))+" --json -" {
		t.Fatalf("answer cmd = %q, want cc-runtime rpc 'interaction.answer' --json -", call.Cmd)
	}
	var sent interaction.AnswerPayload
	if err := json.Unmarshal([]byte(call.Stdin), &sent); err != nil {
		t.Fatalf("answer stdin is not valid JSON: %v", err)
	}
	if sent.SubjectID != opsSubjID || sent.QuestionID != 9 || len(sent.Selected) != 1 || sent.Selected[0] != "Rewrite" || sent.Notes != "go" {
		t.Fatalf("sent payload = %+v, want the full answer preserved", sent)
	}
	for _, secret := range []string{"Rewrite", "go", opsSubjID} {
		if strings.Contains(call.Cmd, secret) {
			t.Fatalf("answer cmd %q leaked %q onto argv", call.Cmd, secret)
		}
	}
}

func TestAnswerRemoteErrorReplyFails(t *testing.T) {
	runner := NewMockRunner().OnSSH("interaction.answer",
		replyLine(t, false, "answer targets unknown question", nil), errors.New("exit status 1"))

	_, err := AnswerRemote(context.Background(), runner, host1, interaction.AnswerPayload{SubjectID: opsSubjID, QuestionID: 9})
	if err == nil || !strings.Contains(err.Error(), "unknown question") {
		t.Fatalf("err = %v, want the daemon's rejection surfaced", err)
	}
	if !strings.Contains(err.Error(), "srv") {
		t.Fatalf("err = %v, want the target host named", err)
	}
}

func TestAnswerRemoteUnreachableFails(t *testing.T) {
	runner := NewMockRunner().OnSSH("interaction.answer", "", errors.New("ssh: connect: Connection refused"))

	_, err := AnswerRemote(context.Background(), runner, host1, interaction.AnswerPayload{SubjectID: opsSubjID, QuestionID: 9})
	if err == nil || !strings.Contains(err.Error(), "answer on srv") {
		t.Fatalf("err = %v, want an unreachable-answer failure naming the host", err)
	}
}

// sshCmdContains reports whether the runner recorded an ssh remote command
// containing want.
func sshCmdContains(m *MockRunner, want string) bool {
	for _, c := range m.SSHCmdsAll() {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}
