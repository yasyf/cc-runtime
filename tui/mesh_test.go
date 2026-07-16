package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

const (
	meshSelf = "alice@mac.tail.ts.net"
	meshHost = "bob@srv.tail.ts.net"
)

// replyLine builds the daemon.Reply JSON line the rpc passthrough prints.
func replyLine(t *testing.T, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	b, err := json.Marshal(daemon.Reply{OK: true, Body: raw})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	return string(b)
}

func listReplyLine(t *testing.T, port int, subjects ...interaction.ListedSubject) string {
	t.Helper()
	return replyLine(t, map[string]any{"subjects": subjects, "http_port": port})
}

func pendingReplyLine(t *testing.T, questions ...interaction.PendingQuestion) string {
	t.Helper()
	return replyLine(t, map[string]any{"questions": questions})
}

func answerReplyLine(t *testing.T, idled bool) string {
	t.Helper()
	return replyLine(t, map[string]bool{"idled": idled})
}

// newRemoteModel builds a mesh-enabled model already resolved to a subject that
// lives on meshHost, with dial handing back a single scripted runner.
func newRemoteModel(t *testing.T, dial func(string) mesh.Runner) Model {
	t.Helper()
	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &mesh.Registry{Self: meshSelf, Hosts: []string{meshHost}}
	m.local = mesh.NewMockRunner()
	m.dial = dial
	m.resolved = true
	m.res = resolution{SubjectID: "subj-remote", Host: meshHost, HTTPPort: 4002}
	return m
}

func awaiting(id string) interaction.ListedSubject {
	return interaction.ListedSubject{SubjectID: id, Status: interaction.StatusAwaiting, Pending: 1}
}

func idle(id string) interaction.ListedSubject {
	return interaction.ListedSubject{SubjectID: id, Status: interaction.StatusIdle}
}

func TestPickMeshAwaitingResolvesRemote(t *testing.T) {
	results := []mesh.HostSubjects{
		{Host: meshSelf, Local: true, Subjects: []interaction.ListedSubject{idle("l-1")}, HTTPPort: 4001},
		{Host: meshHost, Local: false, Subjects: []interaction.ListedSubject{awaiting("r-1")}, HTTPPort: 4002},
	}
	res, found, multiple := pickMeshAwaiting(results)
	if !found || multiple {
		t.Fatalf("found=%v multiple=%v, want found on the single remote awaiting subject", found, multiple)
	}
	if res.SubjectID != "r-1" || res.Host != meshHost || res.Local {
		t.Fatalf("res = %+v, want the remote subject r-1 on %s", res, meshHost)
	}
	if res.HTTPPort != 4002 {
		t.Fatalf("res.HTTPPort = %d, want the owning host's port 4002", res.HTTPPort)
	}
}

func TestPickMeshAwaitingResolvesLocal(t *testing.T) {
	results := []mesh.HostSubjects{
		{Host: meshSelf, Local: true, Subjects: []interaction.ListedSubject{awaiting("l-1")}, HTTPPort: 4001},
		{Host: meshHost, Local: false, Subjects: []interaction.ListedSubject{idle("r-1")}, HTTPPort: 4002},
	}
	res, found, _ := pickMeshAwaiting(results)
	if !found || res.SubjectID != "l-1" || !res.Local {
		t.Fatalf("res = %+v found=%v, want the local awaiting subject l-1", res, found)
	}
}

func TestPickMeshAwaitingMultipleAcrossMachines(t *testing.T) {
	results := []mesh.HostSubjects{
		{Host: meshSelf, Local: true, Subjects: []interaction.ListedSubject{awaiting("l-1")}},
		{Host: meshHost, Local: false, Subjects: []interaction.ListedSubject{awaiting("r-1")}},
	}
	_, found, multiple := pickMeshAwaiting(results)
	if found || !multiple {
		t.Fatalf("found=%v multiple=%v, want multiple when two machines are both awaiting", found, multiple)
	}
}

func TestPickMeshAwaitingNoneWithDeadPeerIgnored(t *testing.T) {
	results := []mesh.HostSubjects{
		{Host: meshSelf, Local: true, Subjects: []interaction.ListedSubject{idle("l-1")}},
		{Host: meshHost, Local: false, Err: errors.New("unreachable")},
	}
	_, found, multiple := pickMeshAwaiting(results)
	if found || multiple {
		t.Fatalf("found=%v multiple=%v, want neither: nothing awaiting and the dead peer is ignored", found, multiple)
	}
}

func TestMeshResolveResolvesRemoteAwaiting(t *testing.T) {
	local := mesh.NewMockRunner().On("interaction.list", listReplyLine(t, 4001), nil)
	host := mesh.NewMockRunner().On("interaction.list", listReplyLine(t, 4002, awaiting("r-1")), nil)

	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &mesh.Registry{Self: meshSelf, Hosts: []string{meshHost}}
	m.local = local
	m.dial = func(string) mesh.Runner { return host }

	msg := m.resolveCmd()()
	mm, ok := msg.(meshResolvedMsg)
	if !ok {
		t.Fatalf("resolveCmd produced %T, want meshResolvedMsg", msg)
	}
	if !mm.found || mm.res.SubjectID != "r-1" || mm.res.Host != meshHost {
		t.Fatalf("mesh resolve = %+v, want the remote awaiting subject", mm)
	}

	m = update(t, m, mm)
	if !m.resolved || m.res.SubjectID != "r-1" {
		t.Fatalf("model did not resolve to the remote subject: resolved=%v res=%+v", m.resolved, m.res)
	}
	if len(m.roster) != 2 {
		t.Fatalf("roster = %d hosts, want 2 stored for display", len(m.roster))
	}
}

func TestMeshResolveMalformedReplyFailsLoud(t *testing.T) {
	local := mesh.NewMockRunner().On("interaction.list", listReplyLine(t, 4001), nil)
	host := mesh.NewMockRunner().On("interaction.list", "not a json reply\n", nil)

	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &mesh.Registry{Self: meshSelf, Hosts: []string{meshHost}}
	m.local = local
	m.dial = func(string) mesh.Runner { return host }

	msg := m.resolveCmd()()
	em, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("a malformed peer reply should produce errMsg, got %T", msg)
	}
	if !strings.Contains(em.Err.Error(), "malformed") {
		t.Fatalf("err = %v, want a loud malformed-reply failure", em.Err)
	}
}

func TestRemoteAnswerDispatch(t *testing.T) {
	runner := mesh.NewMockRunner().On("interaction.answer", answerReplyLine(t, true), nil)
	m := newRemoteModel(t, func(string) mesh.Runner { return runner })

	q := interaction.QuestionPayload{Prompt: "which?", Options: []interaction.Option{{Label: "A"}, {Label: "B"}}}
	m = update(t, m, reseededMsg{Questions: []question{{ID: 5, Payload: q}}})
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace}) // select A

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("enter on a resolved remote question should submit")
	}
	sm, ok := cmd().(submittedMsg)
	if !ok {
		t.Fatalf("remote submit produced a non-submittedMsg")
	}
	if sm.Err != nil {
		t.Fatalf("remote answer errored: %v", sm.Err)
	}
	if sm.QuestionID != 5 || !sm.Idled {
		t.Fatalf("submittedMsg = %+v, want question 5 with the remote release (idled)", sm)
	}

	// The answer went out over the runner as a real interaction.answer rpc.
	var sent interaction.AnswerPayload
	found := false
	for _, c := range runner.Calls() {
		if len(c) == 5 && c[2] == string(interaction.OpAnswer) {
			if err := json.Unmarshal([]byte(c[4]), &sent); err != nil {
				t.Fatalf("answer payload not JSON: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no interaction.answer rpc issued; calls = %v", runner.Calls())
	}
	if sent.SubjectID != "subj-remote" || sent.QuestionID != 5 || len(sent.Selected) != 1 || sent.Selected[0] != "A" {
		t.Fatalf("sent answer = %+v, want the resolved remote subject's selection", sent)
	}

	// Feeding the outcome back marks it answered and shows the remote release.
	m = update(t, m, sm)
	if !m.answered[5] || !m.idled {
		t.Fatalf("answered=%v idled=%v, want both after a successful remote answer", m.answered[5], m.idled)
	}
	if !strings.Contains(m.render(), "released srv") {
		t.Fatalf("done view must show the remote release; got:\n%s", m.render())
	}
}

func TestRemoteAnswerErrorLatches(t *testing.T) {
	runner := mesh.NewMockRunner().On("interaction.answer", "", errors.New("ssh: Connection refused"))
	m := newRemoteModel(t, func(string) mesh.Runner { return runner })

	q := interaction.QuestionPayload{Prompt: "which?", Options: []interaction.Option{{Label: "A"}}}
	m = update(t, m, reseededMsg{Questions: []question{{ID: 5, Payload: q}}})
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm, ok := cmd().(submittedMsg)
	if !ok || sm.Err == nil {
		t.Fatalf("a failed remote answer should carry an error; got %+v (ok=%v)", sm, ok)
	}
	m = update(t, m, sm)
	if !showsError(m) {
		t.Fatal("a failed remote answer should latch into the error screen")
	}
	if m.answered[5] {
		t.Fatal("a failed remote answer must not mark the question answered")
	}
}

func TestRemoteReseedSurfacesPendingQuestions(t *testing.T) {
	q := interaction.QuestionPayload{Prompt: "remote q", Options: []interaction.Option{{Label: "A"}}}
	payload, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal question: %v", err)
	}
	local := mesh.NewMockRunner().On("interaction.pending", pendingReplyLine(t), nil)
	host := mesh.NewMockRunner().On("interaction.pending",
		pendingReplyLine(t, interaction.PendingQuestion{QuestionID: 8, Payload: string(payload)}), nil)

	m := newRemoteModel(t, func(string) mesh.Runner { return host })
	m.local = local

	msg := m.reseedCmd()()
	rm, ok := msg.(reseededMsg)
	if !ok {
		t.Fatalf("remote reseed produced %T, want reseededMsg", msg)
	}
	if len(rm.Questions) != 1 || rm.Questions[0].ID != 8 {
		t.Fatalf("reseed questions = %+v, want the remote host's open question 8", rm.Questions)
	}

	m = update(t, m, rm)
	fq, ok := m.focused()
	if !ok || fq.ID != 8 {
		t.Fatalf("remote reseed did not surface the open question: focus=%+v ok=%v", fq, ok)
	}
	if !hostCalled(host, "subj-remote") {
		t.Fatalf("pending rpc did not carry the resolved subject id; calls = %v", host.Calls())
	}
}

func TestRosterRendersMachineColumns(t *testing.T) {
	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &mesh.Registry{Self: meshSelf, Hosts: []string{meshHost}}
	m.local = mesh.NewMockRunner()
	m.dial = func(string) mesh.Runner { return mesh.NewMockRunner() }
	m.roster = []mesh.HostSubjects{
		{Host: meshSelf, Local: true, Subjects: []interaction.ListedSubject{awaiting("local-abc123def")}},
		{Host: meshHost, Local: false, Err: errors.New("down")},
	}

	out := m.render()
	for _, want := range []string{"— mesh —", "local", "awaiting", "srv", "unreachable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("roster render missing %q:\n%s", want, out)
		}
	}
}

func TestLocalOnlyPathHasNoMeshChrome(t *testing.T) {
	m := newTestModel("subject-xyz")
	if m.meshEnabled() {
		t.Fatal("a model with no registry must not be mesh-enabled")
	}
	m = update(t, m, questionFrame(t, 1, "Q1"))

	out := m.render()
	if strings.Contains(out, "— mesh —") || strings.Contains(out, " · local · ") {
		t.Fatalf("local-only render must carry no mesh chrome:\n%s", out)
	}
	if !strings.Contains(out, "· subject subject-xyz") {
		t.Fatalf("local-only header lost its original shape:\n%s", out)
	}
}

// hostCalled reports whether the runner recorded a call carrying want.
func hostCalled(m *mesh.MockRunner, want string) bool {
	for _, c := range m.Calls() {
		for _, a := range c {
			if strings.Contains(a, want) {
				return true
			}
		}
	}
	return false
}
