package interaction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/daemon"
	dkdaemon "github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/paths"
	"github.com/yasyf/daemonkit/trust"
	"github.com/yasyf/daemonkit/wire"
)

// shortTempHome returns a short-pathed temp HOME so the daemon's unix socket
// path stays under the macOS sun_path limit (~104 bytes), which t.TempDir()'s
// long /var/folders path would blow past.
func shortTempHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccr")
	if err != nil {
		t.Fatalf("mkdir temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Clean(dir)
}

func testRoles() daemon.Roles {
	return daemon.Roles{
		Business: trust.UnprotectedRole, Lifecycle: "com.yasyf.cc-runtime.test.lifecycle.v1",
		StopControl: "com.yasyf.cc-runtime.test.stop.v1",
	}
}

func testTrustPolicy(t *testing.T) trust.TrustPolicy {
	t.Helper()
	roles := testRoles()
	policy, err := trust.NewTrustPolicy(trust.TrustPolicyConfig{
		ExpectedUID: os.Geteuid(), AllowUnprotected: true,
		Roles: map[trust.PeerRole]trust.Requirement{
			roles.Lifecycle:   {TeamID: "TESTTEAM", SigningIdentifier: "com.yasyf.cc-runtime.test.lifecycle"},
			roles.StopControl: {TeamID: "TESTTEAM", SigningIdentifier: "com.yasyf.cc-runtime.test.stop"},
		},
		StopRoles: []trust.PeerRole{roles.StopControl}, ReceiptRoles: []trust.PeerRole{roles.Lifecycle},
		ReadinessRoles: []trust.PeerRole{roles.Lifecycle},
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func waitReadyClient(t *testing.T, p paths.Paths, runtimeBuild string) *daemon.Client {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var (
		client *daemon.Client
		health daemon.RuntimeHealth
		err    error
	)
	for {
		if client == nil {
			client, err = daemon.NewClient(context.Background(), daemon.ClientConfig{
				Socket: p.SocketPath(), WireBuild: daemon.WireBuild, Role: trust.UnprotectedRole,
			})
		}
		if err == nil {
			health, err = client.RuntimeHealth(context.Background())
			if err == nil && health.RuntimeBuild == runtimeBuild &&
				health.RuntimeProtocol == int(wire.ProtocolVersion) && health.ProcessGeneration != "" &&
				health.Ready && health.State == dkdaemon.StateHealthy && !health.Draining {
				t.Cleanup(func() { _ = client.Close() })
				return client
			}
		}
		if time.Now().After(deadline) {
			if client != nil {
				_ = client.Close()
			}
			t.Fatalf("daemon readiness: health=%+v err=%v", health, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const (
	testSession = "sess-1"
	testPID     = 4242
	testScope   = "/scope/a"
)

// recordingFanout captures every fan-out hook invocation for assertions.
type recordingFanout struct {
	mu            sync.Mutex
	questions     []fanoutQuestion
	notifications []fanoutNotification
	dropNext      bool
}

type fanoutQuestion struct {
	subjectID string
	eventID   int64
	q         QuestionPayload
}

type fanoutNotification struct {
	subjectID string
	eventID   int64
	n         NotificationPayload
}

func (f *recordingFanout) Question(subjectID string, eventID int64, q QuestionPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questions = append(f.questions, fanoutQuestion{subjectID: subjectID, eventID: eventID, q: q})
}

func (f *recordingFanout) Notification(subjectID string, eventID int64, n NotificationPayload, complete func(error)) {
	f.mu.Lock()
	if f.dropNext {
		f.dropNext = false
		f.mu.Unlock()
		return
	}
	f.notifications = append(f.notifications, fanoutNotification{subjectID: subjectID, eventID: eventID, n: n})
	f.mu.Unlock()
	if complete != nil {
		complete(nil)
	}
}

func (f *recordingFanout) dropNextNotification() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropNext = true
}

func (f *recordingFanout) snapshot() ([]fanoutQuestion, []fanoutNotification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fanoutQuestion(nil), f.questions...), append([]fanoutNotification(nil), f.notifications...)
}

// harness is a real daemon driven over its unix socket: handlers run against an
// ephemeral on-disk SQLite the daemon owns, exactly as in production.
type harness struct {
	t      *testing.T
	client *daemon.Client
	paths  paths.Paths
	fanout *recordingFanout
	server *daemon.Server
}

func newHarness(t *testing.T, tweaks ...func(*daemon.Config)) *harness {
	t.Helper()
	t.Setenv("HOME", shortTempHome(t))
	p := paths.Paths{App: ".ccr"}

	cfg := daemon.Config{
		AppName:         "cc-runtime-test",
		Paths:           p,
		WireBuild:       daemon.WireBuild,
		RuntimeBuild:    "v0.0.0-test",
		TrustPolicy:     testTrustPolicy(t),
		Roles:           testRoles(),
		ActiveStatuses:  ActiveStatuses,
		Gate:            Gate(),
		GateErrorReason: GateErrorReason,
		StoreSchema:     StoreSchema,
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	s, err := daemon.New(cfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	fanout := &recordingFanout{}
	Register(s, fanout)
	MountREST(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop within 5s")
		}
	})

	client := waitReadyClient(t, p, cfg.RuntimeBuild)
	return &harness{t: t, client: client, paths: p, fanout: fanout, server: s}
}

func (h *harness) do(env daemon.Envelope) daemon.Reply {
	h.t.Helper()
	r, err := h.client.Do(context.Background(), env)
	if err != nil {
		h.t.Fatalf("Do(%s): %v", env.Op, err)
	}
	return r
}

func (h *harness) agentEnv(op daemon.Op, body any) daemon.Envelope {
	h.t.Helper()
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	return daemon.Envelope{Op: op, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: raw}
}

func (h *harness) start() string {
	h.t.Helper()
	r := h.do(h.agentEnv(OpStart, nil))
	if !r.OK {
		h.t.Fatalf("start: %s", r.Error)
	}
	return r.SubjectID
}

func (h *harness) ask(q QuestionPayload) (string, int64) {
	h.t.Helper()
	r := h.do(h.agentEnv(OpAsk, q))
	if !r.OK {
		h.t.Fatalf("ask: %s", r.Error)
	}
	var reply askReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		h.t.Fatalf("unmarshal ask reply: %v", err)
	}
	return reply.SubjectID, reply.QuestionID
}

func (h *harness) answer(subjectID string, questionID int64) bool {
	h.t.Helper()
	body, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: questionID, Selected: []string{"yes"}})
	r := h.do(daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: body})
	if !r.OK {
		h.t.Fatalf("answer: %s", r.Error)
	}
	var reply answerReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		h.t.Fatalf("unmarshal answer reply: %v", err)
	}
	return reply.Idled
}

func (h *harness) status(subjectID string) string {
	h.t.Helper()
	var got string
	if err := h.queryRow(subjectID, &got); err != nil {
		h.t.Fatalf("read status: %v", err)
	}
	return got
}

// queryRow reads a subject's status through the list op, the only read surface
// the package exposes over the socket.
func (h *harness) queryRow(subjectID string, dst *string) error {
	r := h.do(daemon.Envelope{Op: OpList, Scope: testScope})
	var reply listReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		return err
	}
	for _, s := range reply.Subjects {
		if s.SubjectID == subjectID {
			*dst = s.Status
			return nil
		}
	}
	*dst = StatusClosed
	return nil
}

func (h *harness) gateAllows() bool {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"tool_name": "Edit", "tool_input": json.RawMessage(`{"file_path":"x.go"}`)})
	r := h.do(daemon.Envelope{Op: daemon.OpGuardEdit, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: body})
	if !r.OK {
		h.t.Fatalf("guard-edit: %s", r.Error)
	}
	return r.Allow
}

func sampleQuestion(header string) QuestionPayload {
	return QuestionPayload{
		Header:  header,
		Prompt:  "pick one",
		Options: []Option{{Label: "yes"}, {Label: "no"}},
	}
}

func TestHandleAskAppendsAwaitsAndProjects(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("deploy?"))

	if subjectID == "" || questionID != 1 {
		t.Fatalf("ask = (%q, %d), want a subject id and question_id 1 (first event seq)", subjectID, questionID)
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after ask = %q, want awaiting", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must block edits while awaiting")
	}

	pendBody, _ := json.Marshal(subjectBody{SubjectID: subjectID})
	r := h.do(daemon.Envelope{Op: OpPending, Scope: testScope, Body: pendBody})
	var pend pendingReply
	if err := json.Unmarshal(r.Body, &pend); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if len(pend.Questions) != 1 || pend.Questions[0].QuestionID != questionID || pend.Questions[0].Header != "deploy?" {
		t.Fatalf("pending = %+v, want one question id %d header deploy?", pend.Questions, questionID)
	}
}

// TestHandleAskRejectsEmptyPromptAndOptions pins the empty-shape guard: an
// empty ask would engage the edit gate on nothing the human can answer.
func TestHandleAskRejectsEmptyPromptAndOptions(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		id   string
		q    QuestionPayload
		want string
	}{
		{"empty options", QuestionPayload{Prompt: "pick one"}, "option"},
		{"empty prompt", QuestionPayload{Options: []Option{{Label: "yes"}}}, "prompt"},
		{"empty everything", QuestionPayload{}, "prompt"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := h.do(h.agentEnv(OpAsk, tc.q))
			if r.OK || !strings.Contains(r.Error, tc.want) {
				t.Fatalf("ask reply = %+v, want a rejection naming the empty %s", r, tc.want)
			}
			if r.SubjectID != "" {
				t.Fatalf("rejected ask still resolved subject %q", r.SubjectID)
			}
		})
	}
	if questions, _ := h.fanout.snapshot(); len(questions) != 0 {
		t.Fatalf("rejected asks still fanned out: %+v", questions)
	}
}

func TestHandleNotifyRejectsEmptyMessage(t *testing.T) {
	h := newHarness(t)
	r := h.do(h.agentEnv(OpNotify, NotificationPayload{}))
	if r.OK || !strings.Contains(r.Error, "message") {
		t.Fatalf("notify reply = %+v, want an empty-message rejection", r)
	}
	if _, notifications := h.fanout.snapshot(); len(notifications) != 0 {
		t.Fatalf("rejected notify still fanned out: %+v", notifications)
	}
}

func TestHandleNotifyDoesNotChangeStatus(t *testing.T) {
	h := newHarness(t)
	subjectID := h.start()
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after start = %q, want idle", got)
	}

	r := h.do(h.agentEnv(OpNotify, NotificationPayload{Message: "heads up"}))
	if !r.OK {
		t.Fatalf("notify: %s", r.Error)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after notify = %q, want unchanged idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits when idle")
	}
}

func TestHandleAnswerOnlyQuestionIdles(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("ship?"))

	if idled := h.answer(subjectID, questionID); !idled {
		t.Fatal("answering the only open question must idle the subject")
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after answer = %q, want idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits after the subject idles")
	}
}

func TestMultiAskStaysAwaitingUntilAllAnswered(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	_, q2 := h.ask(sampleQuestion("q2"))

	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status with two open = %q, want awaiting", got)
	}

	if idled := h.answer(subjectID, q1); idled {
		t.Fatal("answering q1 must not idle while q2 is still open")
	}
	if got := h.status(subjectID); got != StatusAwaiting {
		t.Fatalf("status after answering q1 = %q, want still awaiting", got)
	}
	if h.gateAllows() {
		t.Fatal("gate must still block while q2 is open")
	}

	if idled := h.answer(subjectID, q2); !idled {
		t.Fatal("answering the last open question must idle the subject")
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("status after answering q2 = %q, want idle", got)
	}
	if !h.gateAllows() {
		t.Fatal("gate must allow edits once all questions are answered")
	}
}

func TestConcurrentAnswerRaceIdlesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	subjectID, q1 := h.ask(sampleQuestion("q1"))
	_, q2 := h.ask(sampleQuestion("q2"))

	var wg sync.WaitGroup
	var mu sync.Mutex
	idledCount := 0
	answers := []int64{q1, q2}
	for _, qid := range answers {
		wg.Add(1)
		go func(qid int64) {
			defer wg.Done()
			body, _ := json.Marshal(AnswerPayload{SubjectID: subjectID, QuestionID: qid, Selected: []string{"yes"}})
			r, err := h.client.Do(context.Background(), daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: body})
			if err != nil {
				t.Errorf("concurrent answer: %v", err)
				return
			}
			var reply answerReply
			if err := json.Unmarshal(r.Body, &reply); err != nil {
				t.Errorf("unmarshal concurrent answer: %v", err)
				return
			}
			if reply.Idled {
				mu.Lock()
				idledCount++
				mu.Unlock()
			}
		}(qid)
	}
	wg.Wait()

	if idledCount != 1 {
		t.Fatalf("idled reported %d times, want exactly once", idledCount)
	}
	if got := h.status(subjectID); got != StatusIdle {
		t.Fatalf("final status = %q, want idle", got)
	}
}

func TestHandleAnswerPollReportsAnswer(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("color?"))

	pollBodyJSON, _ := json.Marshal(pollBody{SubjectID: subjectID, QuestionID: questionID})
	r := h.do(daemon.Envelope{Op: OpAnswerPoll, Scope: testScope, Body: pollBodyJSON})
	var before pollReply
	if err := json.Unmarshal(r.Body, &before); err != nil {
		t.Fatalf("unmarshal poll before: %v", err)
	}
	if before.Answered {
		t.Fatalf("poll before answer = %+v, want answered false", before)
	}

	h.answer(subjectID, questionID)

	r = h.do(daemon.Envelope{Op: OpAnswerPoll, Scope: testScope, Body: pollBodyJSON})
	var after pollReply
	if err := json.Unmarshal(r.Body, &after); err != nil {
		t.Fatalf("unmarshal poll after: %v", err)
	}
	if !after.Answered {
		t.Fatalf("poll after answer = %+v, want answered true", after)
	}
	var ans AnswerPayload
	if err := json.Unmarshal(after.Answer, &ans); err != nil {
		t.Fatalf("unmarshal poll answer payload: %v", err)
	}
	if ans.QuestionID != questionID || len(ans.Selected) != 1 || ans.Selected[0] != "yes" {
		t.Fatalf("poll answer = %+v, want question %d selected [yes]", ans, questionID)
	}
}

func TestFanoutFiresOnDurableAppendsOnly(t *testing.T) {
	h := newHarness(t)
	subjectID, questionID := h.ask(sampleQuestion("deploy?"))

	questions, notifications := h.fanout.snapshot()
	if len(questions) != 1 || len(notifications) != 0 {
		t.Fatalf("after ask: %d questions, %d notifications fanned out, want 1 and 0", len(questions), len(notifications))
	}
	if questions[0].subjectID != subjectID || questions[0].eventID != questionID || questions[0].q.Header != "deploy?" || questions[0].q.Prompt != "pick one" {
		t.Fatalf("fanned-out question = %+v, want subject %s header deploy? prompt pick one", questions[0], subjectID)
	}

	r := h.do(h.agentEnv(OpNotify, NotificationPayload{Message: "heads up", Urgency: "low"}))
	if !r.OK {
		t.Fatalf("notify: %s", r.Error)
	}
	questions, notifications = h.fanout.snapshot()
	if len(questions) != 1 || len(notifications) != 1 {
		t.Fatalf("after notify: %d questions, %d notifications fanned out, want 1 and 1", len(questions), len(notifications))
	}
	if notifications[0].subjectID != subjectID || notifications[0].n.Message != "heads up" || notifications[0].n.Urgency != "low" {
		t.Fatalf("fanned-out notification = %+v, want subject %s message heads up urgency low", notifications[0], subjectID)
	}

	r = h.do(h.agentEnv(OpCaptureQuestion, QuestionPayload{Header: "native", Prompt: "which?"}))
	if !r.OK {
		t.Fatalf("capture-question: %s", r.Error)
	}
	questions, notifications = h.fanout.snapshot()
	if len(questions) != 1 || len(notifications) != 2 {
		t.Fatalf("after capture-question: %d questions, %d notifications fanned out, want 1 and 2 (a captured question mirrors as a notification)", len(questions), len(notifications))
	}
	if notifications[1].n.Message != "native: which?" {
		t.Fatalf("captured-question fanout message = %q, want %q", notifications[1].n.Message, "native: which?")
	}

	h.answer(subjectID, questionID)
	questions, notifications = h.fanout.snapshot()
	if len(questions) != 1 || len(notifications) != 2 {
		t.Fatalf("after answer: %d questions, %d notifications fanned out, want unchanged 1 and 2 (answers never fan out)", len(questions), len(notifications))
	}
}

func TestHandleNotifyDeduplicatesStableDeliveryKey(t *testing.T) {
	h := newHarness(t)
	n := NotificationPayload{Message: "routed", DeliveryKey: "routed:origin:subject:7"}

	first := h.do(h.agentEnv(OpNotify, n))
	second := h.do(h.agentEnv(OpNotify, n))
	if !first.OK || !second.OK {
		t.Fatalf("notify replies = (%+v, %+v), want both successful", first, second)
	}
	_, notifications := h.fanout.snapshot()
	if len(notifications) != 1 {
		t.Fatalf("duplicate delivery fanned out %d times, want exactly once", len(notifications))
	}
	if notifications[0].n.DeliveryKey != n.DeliveryKey || notifications[0].eventID == 0 {
		t.Fatalf("fanned-out notification = %+v, want stable key and durable event id", notifications[0])
	}

	conflict := h.do(h.agentEnv(OpNotify, NotificationPayload{Message: "different", DeliveryKey: n.DeliveryKey}))
	if conflict.OK || !strings.Contains(conflict.Error, "delivery key reused with different payload") {
		t.Fatalf("conflicting delivery key reply = %+v, want payload mismatch", conflict)
	}
}

func TestNotificationOutboxReplaysCrashBetweenClaimAndFanout(t *testing.T) {
	h := newHarness(t)
	h.fanout.dropNextNotification()
	n := NotificationPayload{Message: "routed", DeliveryKey: "routed:origin:subject:8"}

	first := h.do(h.agentEnv(OpNotify, n))
	if !first.OK {
		t.Fatalf("notify: %s", first.Error)
	}
	_, notifications := h.fanout.snapshot()
	if len(notifications) != 0 {
		t.Fatalf("simulated pre-fanout crash delivered %+v", notifications)
	}
	if err := ReplayNotificationDeliveries(t.Context(), h.server.DB(), h.fanout); err != nil {
		t.Fatalf("replay notification deliveries: %v", err)
	}
	_, notifications = h.fanout.snapshot()
	if len(notifications) != 1 || notifications[0].n.DeliveryKey != n.DeliveryKey {
		t.Fatalf("replayed notifications = %+v, want one stable-key delivery", notifications)
	}
	var state string
	if err := h.server.DB().QueryRow(`
SELECT state FROM notification_deliveries
WHERE subject_id = ? AND delivery_key = ?`, first.SubjectID, n.DeliveryKey).Scan(&state); err != nil {
		t.Fatalf("read delivery state: %v", err)
	}
	if state != notificationCompleted {
		t.Fatalf("delivery state = %q, want completed", state)
	}

	retry := h.do(h.agentEnv(OpNotify, n))
	if !retry.OK {
		t.Fatalf("retry notify: %s", retry.Error)
	}
	_, notifications = h.fanout.snapshot()
	if len(notifications) != 1 {
		t.Fatalf("completed delivery retried %d times, want no duplicate", len(notifications))
	}
}

func TestHandleListReturnsAwaitingSubjectWithPendingCount(t *testing.T) {
	h := newHarness(t)
	subjectID, _ := h.ask(sampleQuestion("q1"))
	h.ask(sampleQuestion("q2"))

	r := h.do(daemon.Envelope{Op: OpList, Scope: testScope})
	var reply listReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(reply.Subjects) != 1 {
		t.Fatalf("list = %+v, want one subject", reply.Subjects)
	}
	got := reply.Subjects[0]
	if got.SubjectID != subjectID || got.Status != StatusAwaiting || got.Pending != 2 {
		t.Fatalf("listed subject = %+v, want %s awaiting pending 2", got, subjectID)
	}
}
