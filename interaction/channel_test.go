package interaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/consume"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/event"
)

// channelHarness runs a real daemon on cc-runtime's production state-dir layout
// (AppPaths), so the ask tool's internal client — which dials
// AppPaths().SocketPath() — connects to it. HOME is redirected to a short temp
// dir so the socket path stays under the sun_path limit.
type channelHarness struct {
	t      *testing.T
	client *daemon.Client
	port   int
}

func newChannelHarness(t *testing.T) *channelHarness {
	t.Helper()
	t.Setenv("HOME", shortTempHome(t))
	p := AppPaths()

	s, err := daemon.New(daemon.Config{
		AppName:         AppName,
		Paths:           p,
		Version:         "v0.0.0-test",
		ActiveStatuses:  ActiveStatuses,
		WindowAlive:     func(int) bool { return true },
		Gate:            Gate(),
		GateErrorReason: GateErrorReason,
		Migrate:         Migrate,
	})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	Register(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop within 5s")
		}
	})

	client := daemon.NewClient(p.SocketPath())
	deadline := time.Now().Add(5 * time.Second)
	for !client.Available() {
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}

	r, err := client.Do(context.Background(), daemon.Envelope{Op: OpList, Scope: testScope})
	if err != nil {
		t.Fatalf("list for http port: %v", err)
	}
	var lr listReply
	if err := json.Unmarshal(r.Body, &lr); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	return &channelHarness{t: t, client: client, port: lr.HTTPPort}
}

func askTool(t *testing.T) channel.Tool {
	t.Helper()
	tools, method, instructions, err := ChannelTools(context.Background(), testSession, testScope)
	if err != nil {
		t.Fatalf("ChannelTools: %v", err)
	}
	if method != notifyMethod {
		t.Fatalf("notify method = %q, want %q", method, notifyMethod)
	}
	if !strings.Contains(instructions, "AskUserQuestion") {
		t.Fatalf("instructions missing AskUserQuestion steer: %q", instructions)
	}
	for _, tl := range tools {
		if tl.Name == "ask" {
			return tl
		}
	}
	t.Fatal("ask tool not advertised")
	return channel.Tool{}
}

func submitAnswerFor(t *testing.T, client *daemon.Client, selected ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, err := client.Do(context.Background(), daemon.Envelope{Op: OpList, Scope: testScope})
		if err != nil {
			t.Errorf("list awaiting: %v", err)
			return
		}
		var lr listReply
		if err := json.Unmarshal(r.Body, &lr); err != nil {
			t.Errorf("unmarshal list: %v", err)
			return
		}
		for _, s := range lr.Subjects {
			if s.Status != StatusAwaiting {
				continue
			}
			pb, _ := json.Marshal(subjectBody{SubjectID: s.SubjectID})
			pr := mustDo(t, client, daemon.Envelope{Op: OpPending, Scope: testScope, Body: pb})
			var pend pendingReply
			if err := json.Unmarshal(pr.Body, &pend); err != nil {
				t.Errorf("unmarshal pending: %v", err)
				return
			}
			if len(pend.Questions) == 0 {
				continue
			}
			ab, _ := json.Marshal(AnswerPayload{SubjectID: s.SubjectID, QuestionID: pend.Questions[0].QuestionID, Selected: selected})
			mustDo(t, client, daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: ab})
			return
		}
		if time.Now().After(deadline) {
			t.Error("no awaiting question appeared to answer")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustDo(t *testing.T, client *daemon.Client, env daemon.Envelope) daemon.Reply {
	t.Helper()
	r, err := client.Do(context.Background(), env)
	if err != nil {
		t.Fatalf("Do(%s): %v", env.Op, err)
	}
	if !r.OK {
		t.Fatalf("Do(%s): %s", env.Op, r.Error)
	}
	return r
}

func sampleArgs(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(QuestionPayload{
		Header:  "approach",
		Prompt:  "pick one",
		Options: []Option{{Label: "ship"}, {Label: "hold"}},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

func TestAskToolLongPollReturnsHumanAnswer(t *testing.T) {
	h := newChannelHarness(t)
	ask := askTool(t)

	go submitAnswerFor(t, h.client, "ship")

	text, isErr := ask.Handler(context.Background(), sampleArgs(t))
	if isErr {
		t.Fatalf("ask handler returned error: %q", text)
	}
	if !strings.Contains(text, "ship") {
		t.Fatalf("ask result = %q, want the human's selection 'ship'", text)
	}
}

func TestAskToolDegradesToPendingWithoutError(t *testing.T) {
	h := newChannelHarness(t)

	// Drive a question directly so the inline long-poll has a target, then let an
	// injected budget expire with no answer ever arriving.
	r := mustDo(t, h.client, daemon.Envelope{Op: OpAsk, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: sampleArgs(t)})
	var ar askReply
	if err := json.Unmarshal(r.Body, &ar); err != nil {
		t.Fatalf("unmarshal ask reply: %v", err)
	}

	text, err := awaitAnswer(context.Background(), h.client, testSession, testScope, testPID,
		pollBody{SubjectID: ar.SubjectID, QuestionID: ar.QuestionID}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitAnswer errored on budget expiry: %v", err)
	}
	if text != "" {
		t.Fatalf("awaitAnswer = %q on budget expiry, want empty so the handler renders the non-error pending string", text)
	}
}

func TestHumanAnswerNotSuppressedByExcludeAgentOrigin(t *testing.T) {
	h := newChannelHarness(t)

	r := mustDo(t, h.client, daemon.Envelope{Op: OpAsk, Session: testSession, ClaudePID: testPID, Scope: testScope, Body: sampleArgs(t)})
	var ar askReply
	if err := json.Unmarshal(r.Body, &ar); err != nil {
		t.Fatalf("unmarshal ask reply: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gotAnswer := make(chan string, 1)
	go func() {
		_ = consume.ConsumeEvents(ctx, consume.StreamSource{
			Port:          h.port,
			SubjectID:     ar.SubjectID,
			Consumer:      "test-watch",
			ExcludeOrigin: event.OriginAgent,
			Paths:         AppPaths(),
		}, func(_ int64, data string) (bool, error) {
			var e struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(data), &e)
			if e.Type == EventAnswer {
				gotAnswer <- data
				return true, nil
			}
			return false, nil
		})
	}()

	ab, _ := json.Marshal(AnswerPayload{SubjectID: ar.SubjectID, QuestionID: ar.QuestionID, Selected: []string{"hold"}})
	mustDo(t, h.client, daemon.Envelope{Op: OpAnswer, Scope: testScope, Body: ab})

	select {
	case data := <-gotAnswer:
		if !strings.Contains(data, "hold") {
			t.Fatalf("streamed answer = %q, want the human's 'hold' selection", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OriginHuman answer was suppressed by ExcludeOrigin=OriginAgent; it must stream through")
	}
}
