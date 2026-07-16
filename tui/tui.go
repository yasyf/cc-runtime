// Package tui is cc-runtime's human answer surface: a Bubble Tea terminal app
// that resolves the awaiting subject for a scope, streams the agent's questions
// and notifications off the daemon's events plane, and submits the human's answer
// back over the control socket.
package tui

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/consume"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

// eventBuffer sizes the channel between the SSE consumer goroutine and the
// Update loop, so a burst of events never blocks the stream reader.
const eventBuffer = 64

// TUICmd is the cc-runtime answer surface. It cold-starts the daemon, polls
// OpList until a subject is awaiting, then streams that subject's events into a
// Bubble Tea model and submits answers over the socket.
func TUICmd(d cmd.Deps) *cobra.Command {
	var cwd string
	c := &cobra.Command{
		Use:   "tui",
		Short: "Answer the agent's questions in an interactive terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if err := d.EnsureCurrent(ctx); err != nil {
				return err
			}
			scope := mustCwd(cwd)
			return run(ctx, d, scope)
		},
	}
	c.Flags().StringVar(&cwd, "cwd", "", "working directory / scope (defaults to the current directory)")
	return c
}

// run wires the events channel, the consumer launcher, and the model, then runs
// the program. The consumer launches once the model resolves the subject, so the
// TUI works whether the agent asks before or after the human opens it.
func run(ctx context.Context, d cmd.Deps, scope string) error {
	events := make(chan liveEvent, eventBuffer)
	client := d.NewClient()

	model := NewModel(scope, client, events)
	// Only a local subject has a reachable events plane; a remote one has no SSE and
	// is fed by the pending reseed instead.
	model.startStream = func(res resolution) {
		if res.Local {
			go streamInto(ctx, d, scope, res, events)
		}
	}
	if err := wireMesh(&model, d); err != nil {
		return err
	}

	p := tea.NewProgram(model, tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// wireMesh enables the mesh paths when the registry has peers: the resolve poll
// then fans interaction.list across every machine and a remote subject is
// answered over ssh. With no peers the model keeps its untouched local-only path.
// A corrupt registry fails loud rather than silently dropping the mesh.
func wireMesh(model *Model, d cmd.Deps) error {
	reg, err := (mesh.Store{Dir: d.Paths.StateDir()}).Load()
	if err != nil {
		return err
	}
	if len(reg.Hosts) == 0 {
		return nil
	}
	model.reg = reg
	model.local = mesh.LocalRunner{}
	model.dial = func(target string) mesh.Runner { return mesh.SSHRunner{Target: target} }
	return nil
}

// streamInto consumes the subject's event log off the daemon's HTTP plane and
// forwards every decoded question and notification frame onto the events channel.
// It carries NO ExcludeOrigin: the TUI wants the agent's questions AND its
// notifications. The TUI is a pid-less consumer (it lives outside any Claude
// window), so the stream never self-terminates on window death.
func streamInto(ctx context.Context, d cmd.Deps, scope string, res resolution, events chan<- liveEvent) {
	defer close(events)
	client := d.NewClient()
	src := consume.StreamSource{
		Port:      res.HTTPPort,
		SubjectID: res.SubjectID,
		Consumer:  tuiConsumer,
		Paths:     d.Paths,
		Refresh:   refreshPort(client, scope),
	}
	err := consume.ConsumeEvents(ctx, src, func(seq int64, data string) (bool, error) {
		typ := eventType(data)
		switch typ {
		case interaction.EventQuestion, interaction.EventNotification:
			select {
			case events <- liveEvent{Seq: seq, Type: typ, Data: data}:
			case <-ctx.Done():
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil && ctx.Err() == nil {
		select {
		case events <- liveEvent{Err: err}:
		case <-ctx.Done():
		}
	}
}

// refreshPort re-resolves the daemon's current HTTP port through OpList so the
// stream survives a version-skew daemon swap underneath it.
func refreshPort(client *daemon.Client, scope string) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		return listPort(ctx, client, scope)
	}
}

// mustCwd returns cwd, defaulting to the process working directory.
func mustCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	d, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return d
}
