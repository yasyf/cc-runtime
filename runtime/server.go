package runtime

import (
	"context"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

// buildServer composes the cc-runtime daemon: the interaction ops, the edit
// gate, the projection schema, and the channel presence lifecycle.
func buildServer() (*daemon.Server, error) {
	conn := channel.Connectivity{}
	s, err := daemon.New(daemon.Config{
		AppName:           interaction.AppName,
		Paths:             interaction.AppPaths(),
		Version:           version.Version,
		ActiveStatuses:    interaction.ActiveStatuses,
		WindowAlive:       live,
		Gate:              interaction.Gate(),
		GateErrorReason:   interaction.GateErrorReason,
		Migrate:           interaction.Migrate,
		PresenceEventType: conn.Type(),
		OnPresenceChange:  conn.OnPresenceChange,
		BootReconcile:     conn.BootReconcile,
	})
	if err != nil {
		return nil, err
	}
	interaction.Register(s)
	return s, nil
}

func serve(ctx context.Context) error {
	s, err := buildServer()
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}
