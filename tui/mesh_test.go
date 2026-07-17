package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

const (
	meshSelf = "alice@mac.tail.ts.net"
	meshHost = "bob@srv.tail.ts.net"
)

// localListKey / localPendingKey are the exact keys mesh.MockRunner.Local
// scripts on for the local fan-out rpc subprocess; the params ride stdin.
func localListKey() string {
	return "cc-runtime rpc interaction.list --json -"
}

func localPendingKey() string {
	return "cc-runtime rpc interaction.pending --json -"
}

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
	m.reg = &hostregistry.Registry{Self: meshSelf, Hosts: []string{meshHost}}
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
	local := mesh.NewMockRunner().OnLocal(localListKey(), listReplyLine(t, 4001), nil)
	host := mesh.NewMockRunner().OnSSH("interaction.list", listReplyLine(t, 4002, awaiting("r-1")), nil)

	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &hostregistry.Registry{Self: meshSelf, Hosts: []string{meshHost}}
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
	// The list rpc went to meshHost's ssh target with the scope on stdin.
	calls := host.SSHCalls(meshHost)
	if len(calls) != 1 {
		t.Fatalf("ssh calls against %q = %d, want 1; all = %v", meshHost, len(calls), host.SSHCmdsAll())
	}
	if !strings.Contains(calls[0].Stdin, "/repo") {
		t.Fatalf("list stdin = %q, want the scope params", calls[0].Stdin)
	}
}

func TestMeshResolveMalformedReplyFailsLoud(t *testing.T) {
	local := mesh.NewMockRunner().OnLocal(localListKey(), listReplyLine(t, 4001), nil)
	host := mesh.NewMockRunner().OnSSH("interaction.list", "not a json reply\n", nil)

	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &hostregistry.Registry{Self: meshSelf, Hosts: []string{meshHost}}
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

// TestPollSkipsWhileResolveInFlight proves a tick never stacks a second resolve
// fan-out behind a slow one: the dispatch is gated until the in-flight poll's
// result lands.
func TestPollSkipsWhileResolveInFlight(t *testing.T) {
	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &hostregistry.Registry{Self: meshSelf, Hosts: []string{meshHost}}
	m.local = mesh.NewMockRunner()
	m.dial = func(string) mesh.Runner { return mesh.NewMockRunner() }

	m = update(t, m, pollTickMsg{})
	if !m.resolving {
		t.Fatal("first tick must mark a resolve in flight")
	}
	next, cmd := m.Update(pollTickMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("a skipped tick must still re-arm the poll")
	}
	// The gated tick keeps ticking without dispatching: the in-flight flag holds.
	if !m.resolving {
		t.Fatal("a skipped tick must not clear the in-flight flag")
	}

	// The poll's result clears the gate so the next tick dispatches again.
	m = update(t, m, meshResolvedMsg{})
	if m.resolving {
		t.Fatal("a landed poll must clear the in-flight flag")
	}
}

func TestRemoteAnswerDispatch(t *testing.T) {
	runner := mesh.NewMockRunner().OnSSH("interaction.answer", answerReplyLine(t, true), nil)
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

	// The answer went to the owning host's ssh target as a single-dial
	// interaction.answer rpc with the payload on stdin, never argv.
	calls := runner.SSHCalls(meshHost)
	if len(calls) != 1 {
		t.Fatalf("ssh calls against %q = %d, want 1; all = %v", meshHost, len(calls), runner.SSHCmdsAll())
	}
	call := calls[0]
	if call.Kind != "ssh" {
		t.Fatalf("answer kind = %q, want the failover ssh leg (answers dedupe daemon-side)", call.Kind)
	}
	if !strings.Contains(call.Cmd, string(interaction.OpAnswer)) {
		t.Fatalf("answer cmd = %q, want the interaction.answer op", call.Cmd)
	}
	var sent interaction.AnswerPayload
	if err := json.Unmarshal([]byte(call.Stdin), &sent); err != nil {
		t.Fatalf("answer stdin not JSON: %v", err)
	}
	if sent.SubjectID != "subj-remote" || sent.QuestionID != 5 || len(sent.Selected) != 1 || sent.Selected[0] != "A" {
		t.Fatalf("sent answer = %+v, want the resolved remote subject's selection", sent)
	}
	if strings.Contains(call.Cmd, "subj-remote") {
		t.Fatalf("answer cmd %q leaked the payload onto argv", call.Cmd)
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
	runner := mesh.NewMockRunner().OnSSH("interaction.answer", "", errors.New("ssh: Connection refused"))
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
	local := mesh.NewMockRunner().OnLocal(localPendingKey(), pendingReplyLine(t), nil)
	host := mesh.NewMockRunner().OnSSH("interaction.pending",
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
	calls := host.SSHCalls(meshHost)
	if len(calls) != 1 || !strings.Contains(calls[0].Stdin, "subj-remote") {
		t.Fatalf("pending rpc did not carry the resolved subject id on stdin; calls = %+v", calls)
	}
}

// TestStaleMeshResolveDoesNotSwapSubject proves a resolve poll that lands after
// resolution cannot overwrite the freshly-selected subject or restart its stream.
func TestStaleMeshResolveDoesNotSwapSubject(t *testing.T) {
	m := newRemoteModel(t, func(string) mesh.Runner { return mesh.NewMockRunner() })
	streamStarts := 0
	m.startStream = func(resolution) { streamStarts++ }

	m = update(t, m, meshResolvedMsg{found: true, res: resolution{SubjectID: "other-subj", Host: meshHost}})
	if m.res.SubjectID != "subj-remote" {
		t.Fatalf("stale resolution swapped the subject to %q", m.res.SubjectID)
	}
	if streamStarts != 0 {
		t.Fatalf("stale resolution restarted the stream %d times", streamStarts)
	}

	m = update(t, m, meshResolvedMsg{multiple: true})
	if m.waitNote != "" {
		t.Fatalf("stale multi-awaiting poll set waitNote %q after resolve", m.waitNote)
	}
}

// TestSameSubjectReresolveDoesNotRestartStream proves a late poll re-resolving
// the already-attached subject never attaches a second consumer to the events
// channel — both consumers would defer-close it.
func TestSameSubjectReresolveDoesNotRestartStream(t *testing.T) {
	m := NewModel("/repo", nil, make(chan liveEvent))
	streamStarts := 0
	m.startStream = func(resolution) { streamStarts++ }

	m = update(t, m, resolvedMsg{Res: resolution{SubjectID: "subj-1", HTTPPort: 4001, Local: true}})
	if streamStarts != 1 {
		t.Fatalf("first resolve started %d streams, want 1", streamStarts)
	}
	m = update(t, m, resolvedMsg{Res: resolution{SubjectID: "subj-1", HTTPPort: 4001, Local: true}})
	if streamStarts != 1 {
		t.Fatalf("same-subject re-resolve restarted the stream (%d starts)", streamStarts)
	}
	if !m.resolved {
		t.Fatal("re-resolve must leave the model resolved")
	}
}

// TestRemoteReseedFailureSurfacesAndRetries proves a transient peer error on the
// remote question fetch is surfaced and retried rather than permanently hiding
// the remote questions.
func TestRemoteReseedFailureSurfacesAndRetries(t *testing.T) {
	failing := mesh.NewMockRunner().OnSSH("interaction.pending", "", errors.New("ssh: connect timed out"))
	m := newRemoteModel(t, func(string) mesh.Runner { return failing })
	m.local = mesh.NewMockRunner().OnLocal(localPendingKey(), pendingReplyLine(t), nil)

	msg := m.reseedCmd()()
	rm, ok := msg.(reseededMsg)
	if !ok || rm.Err == nil {
		t.Fatalf("a failed remote reseed must carry its error; got %#v", msg)
	}

	next, cmd := m.Update(rm)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("a failed remote reseed must schedule a retry")
	}
	if !strings.Contains(m.render(), "retrying") {
		t.Fatalf("failed reseed not surfaced:\n%s", m.render())
	}
	if showsError(m) {
		t.Fatal("a transient reseed failure must not latch the fatal error screen")
	}

	// The peer recovers: the retry re-runs the reseed and surfaces the question.
	q := interaction.QuestionPayload{Prompt: "remote q", Options: []interaction.Option{{Label: "A"}}}
	payload, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal question: %v", err)
	}
	healthy := mesh.NewMockRunner().OnSSH("interaction.pending",
		pendingReplyLine(t, interaction.PendingQuestion{QuestionID: 8, Payload: string(payload)}), nil)
	m.dial = func(string) mesh.Runner { return healthy }

	next, cmd = m.Update(reseedRetryMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("reseedRetryMsg on a resolved model must re-run the reseed")
	}
	m = update(t, m, cmd())
	if fq, ok := m.focused(); !ok || fq.ID != 8 {
		t.Fatalf("recovered reseed did not surface question 8: focus=%+v ok=%v", fq, ok)
	}
	if strings.Contains(m.render(), "retrying") {
		t.Fatal("recovered reseed left the failure note up")
	}
	if m.reseedFails != 0 {
		t.Fatalf("reseedFails = %d, want reset on success", m.reseedFails)
	}
}

// TestReseedRetriesExhaustLatchLoud proves a persistently failing remote reseed
// stops retrying after maxReseedFails and latches the fatal error screen instead
// of silently polling a broken peer forever.
func TestReseedRetriesExhaustLatchLoud(t *testing.T) {
	m := newRemoteModel(t, func(string) mesh.Runner { return mesh.NewMockRunner() })

	fail := reseededMsg{Err: errors.New("malformed interaction.pending reply from srv")}
	for i := 0; i < maxReseedFails-1; i++ {
		next, cmd := m.Update(fail)
		m = next.(Model)
		if cmd == nil {
			t.Fatalf("failure %d must still schedule a retry", i+1)
		}
		if showsError(m) {
			t.Fatalf("failure %d latched early", i+1)
		}
	}
	next, cmd := m.Update(fail)
	m = next.(Model)
	if cmd != nil {
		t.Fatal("the exhausting failure must not schedule another retry")
	}
	if !showsError(m) {
		t.Fatal("exhausted reseed retries must latch the fatal error screen")
	}
}

func TestRosterRendersMachineColumns(t *testing.T) {
	m := NewModel("/repo", nil, make(chan liveEvent))
	m.reg = &hostregistry.Registry{Self: meshSelf, Hosts: []string{meshHost}}
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
