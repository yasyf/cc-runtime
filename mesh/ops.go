package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
)

// localNode is the machine label for this host in a fan-out result when Self
// carries no ssh identity yet.
const localNode = "local"

// maxConcurrentHosts bounds the parallel fan-out.
const maxConcurrentHosts = 8

// HostSubjects is one host's interaction.list result. Err is set (and Subjects
// nil) when the host was unreachable or answered with a failure reply — a benign
// per-host outcome the UI renders as "unreachable", never a whole-fanout failure.
// A malformed reply from a live host is not folded here: it fails the fan-out
// loud (see ListAll).
type HostSubjects struct {
	Host     string
	Local    bool
	Subjects []interaction.ListedSubject
	HTTPPort int
	Err      error
}

// Node is the host's short machine label for display: "local" for this machine,
// the ssh target's node label for a peer.
func (h HostSubjects) Node() string {
	if h.Local {
		return localNode
	}
	return hostregistry.HostNode(h.Host)
}

// HostPending is one host's interaction.pending result for a subject, with the
// same per-host error convention as HostSubjects.
type HostPending struct {
	Host      string
	Local     bool
	Questions []interaction.PendingQuestion
	Err       error
}

// Node mirrors HostSubjects.Node.
func (h HostPending) Node() string {
	if h.Local {
		return localNode
	}
	return hostregistry.HostNode(h.Host)
}

// listParams is the interaction.list rpc body: the scope to list subjects in.
type listParams struct {
	Scope string `json:"scope"`
}

// pendingParams is the interaction.pending rpc body: the subject to list open
// questions for.
type pendingParams struct {
	SubjectID string `json:"subject_id"`
}

// listBody, pendingBody, and answerBody mirror the daemon.Reply.Body shapes the
// interaction handlers return.
type listBody struct {
	Subjects []interaction.ListedSubject `json:"subjects"`
	HTTPPort int                         `json:"http_port"`
}

type pendingBody struct {
	Questions []interaction.PendingQuestion `json:"questions"`
}

type answerBody struct {
	Idled bool `json:"idled"`
}

// fanTarget binds one machine to the runner that reaches it: the local machine
// runs `cc-runtime rpc` locally, a peer over ssh. local selects Local vs SSH on
// the runner.
type fanTarget struct {
	host   string
	local  bool
	runner Runner
}

// fanTargets is Self followed by every peer, in registry order — the merge order
// the fan-out preserves, local first. local is the runner for this machine; dial
// binds each peer to its runner.
func fanTargets(reg *hostregistry.Registry, local Runner, dial func(target string) Runner) []fanTarget {
	out := make([]fanTarget, 0, len(reg.Hosts)+1)
	out = append(out, fanTarget{host: reg.Self, local: true, runner: local})
	for _, h := range reg.Hosts {
		out = append(out, fanTarget{host: h, local: false, runner: dial(h)})
	}
	return out
}

func (t fanTarget) node() string {
	if t.local && t.host == "" {
		return localNode
	}
	return hostregistry.HostNode(t.host)
}

// ListAll fans interaction.list across Self and every peer concurrently — the
// local machine via `cc-runtime rpc interaction.list` (against this host's
// socket), each peer via `ssh target cc-runtime rpc interaction.list` — and
// returns one HostSubjects per machine in merge order (local first). A dead or
// erroring host becomes a per-host {Host, Err} result so one unreachable peer
// never blanks the mesh; a malformed reply from a live host (unparseable envelope
// or body) fails the whole call loud, since that is a protocol break, not a
// reachability blip.
func ListAll(ctx context.Context, reg *hostregistry.Registry, scope string, local Runner, dial func(target string) Runner) ([]HostSubjects, error) {
	targets := fanTargets(reg, local, dial)
	results := make([]HostSubjects, len(targets))
	fatals := make([]error, len(targets))
	fanOut(len(targets), func(i int) {
		results[i], fatals[i] = listOne(ctx, targets[i], scope)
	})
	if err := firstErr(fatals); err != nil {
		return nil, err
	}
	return results, nil
}

func listOne(ctx context.Context, t fanTarget, scope string) (HostSubjects, error) {
	hs := HostSubjects{Host: t.host, Local: t.local}
	reply, raw, err := meshRPC(ctx, t, interaction.OpList, "", listParams{Scope: scope})
	if err != nil {
		if isMalformed(raw) {
			return hs, fmt.Errorf("malformed interaction.list reply from %s: %w", t.node(), err)
		}
		hs.Err = err
		return hs, nil
	}
	if !reply.OK {
		hs.Err = errors.New(reply.Error)
		return hs, nil
	}
	var body listBody
	if err := json.Unmarshal(reply.Body, &body); err != nil {
		return hs, fmt.Errorf("decode interaction.list reply from %s: %w", t.node(), err)
	}
	hs.Subjects = body.Subjects
	hs.HTTPPort = body.HTTPPort
	return hs, nil
}

// PendingAll fans interaction.pending for subjectID across Self and every peer,
// mirroring ListAll's transport, per-host tolerance, and fail-loud contract. A
// subject lives on exactly one machine, so at most one result carries questions;
// the rest come back empty.
func PendingAll(ctx context.Context, reg *hostregistry.Registry, subjectID string, local Runner, dial func(target string) Runner) ([]HostPending, error) {
	targets := fanTargets(reg, local, dial)
	results := make([]HostPending, len(targets))
	fatals := make([]error, len(targets))
	fanOut(len(targets), func(i int) {
		results[i], fatals[i] = pendingOne(ctx, targets[i], subjectID)
	})
	if err := firstErr(fatals); err != nil {
		return nil, err
	}
	return results, nil
}

func pendingOne(ctx context.Context, t fanTarget, subjectID string) (HostPending, error) {
	hp := HostPending{Host: t.host, Local: t.local}
	reply, raw, err := meshRPC(ctx, t, interaction.OpPending, "", pendingParams{SubjectID: subjectID})
	if err != nil {
		if isMalformed(raw) {
			return hp, fmt.Errorf("malformed interaction.pending reply from %s: %w", t.node(), err)
		}
		hp.Err = err
		return hp, nil
	}
	if !reply.OK {
		hp.Err = errors.New(reply.Error)
		return hp, nil
	}
	var body pendingBody
	if err := json.Unmarshal(reply.Body, &body); err != nil {
		return hp, fmt.Errorf("decode interaction.pending reply from %s: %w", t.node(), err)
	}
	hp.Questions = body.Questions
	return hp, nil
}

// AnswerRemote submits an answer to the subject's owning peer over ssh, running
// `cc-runtime rpc interaction.answer` there, and reports whether the answer idled
// the subject (released its edit gate). Unlike the read fan-outs it targets one
// host and returns every failure as an error — an answer that does not land is
// never silently dropped. The failover dial's at-least-once retry is safe here:
// the daemon dedupes a repeated answer per question, keeping the first.
func AnswerRemote(ctx context.Context, r Runner, target string, a interaction.AnswerPayload) (bool, error) {
	reply, _, err := meshRPC(ctx, fanTarget{host: target, runner: r}, interaction.OpAnswer, "", a)
	if err != nil {
		return false, fmt.Errorf("answer on %s: %w", hostregistry.HostNode(target), err)
	}
	if !reply.OK {
		return false, fmt.Errorf("answer on %s: %s", hostregistry.HostNode(target), reply.Error)
	}
	var body answerBody
	if err := json.Unmarshal(reply.Body, &body); err != nil {
		return false, fmt.Errorf("decode interaction.answer reply from %s: %w", hostregistry.HostNode(target), err)
	}
	return body.Idled, nil
}

// meshRPC runs `cc-runtime rpc <op> [--session <s>] --json -` — locally for
// this machine, over ssh for a peer — feeding the JSON params over stdin, and
// decodes the daemon.Reply the passthrough prints. session, when non-empty,
// stamps the envelope's window identity on the peer (the routed-surface path);
// the read fan-outs pass "". It returns raw stdout so the caller can separate
// an unreachable host (no output) from a live host that answered with a
// non-reply (output present) — the malformed case. A decoded reply yields a nil
// error even on a nonzero exit, since the passthrough exits nonzero on an
// OK=false reply the caller reads via reply.OK.
func meshRPC(ctx context.Context, t fanTarget, op daemon.Op, session string, params any) (daemon.Reply, string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return daemon.Reply{}, "", err
	}
	var stdout string
	var runErr error
	switch {
	case t.local:
		args := []string{"rpc", string(op)}
		if session != "" {
			args = append(args, "--session", session)
		}
		stdout, runErr = t.runner.Local(ctx, b, Binary, append(args, "--json", "-")...)
	default:
		stdout, runErr = t.runner.SSH(ctx, t.host, rpcRemoteCmd(op, session), b)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		if runErr != nil {
			return daemon.Reply{}, "", runErr
		}
		return daemon.Reply{}, "", errors.New("empty rpc reply")
	}
	var reply daemon.Reply
	if err := json.Unmarshal([]byte(trimmed), &reply); err != nil {
		return daemon.Reply{}, stdout, err
	}
	return reply, stdout, nil
}

// rpcRemoteCmd is the one-line remote shell command that runs an allowlisted op
// on a peer, reading its JSON params from stdin (`--json -`) so the payload
// never rides the ssh or remote-shell argv.
func rpcRemoteCmd(op daemon.Op, session string) string {
	cmd := Binary + " rpc " + hostregistry.ShellQuote(string(op))
	if session != "" {
		cmd += " --session " + hostregistry.ShellQuote(session)
	}
	return cmd + " --json -"
}

// isMalformed reports whether a failed meshRPC produced output — a live host that
// answered with a non-reply — versus an unreachable host that produced nothing.
func isMalformed(raw string) bool {
	return strings.TrimSpace(raw) != ""
}

// fanOut runs work(i) for each i in [0, n) concurrently, bounded by
// maxConcurrentHosts.
func fanOut(n int, work func(i int)) {
	sem := make(chan struct{}, maxConcurrentHosts)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			work(i)
		}(i)
	}
	wg.Wait()
}

func firstErr(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
