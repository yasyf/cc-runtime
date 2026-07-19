package runtime

import (
	"context"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/paths"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

func appPaths() paths.Paths { return interaction.AppPaths() }

func newClient(ctx context.Context) (*daemon.Client, error) { return launcher().NewClient(ctx) }

func launcher() daemon.Launcher {
	return daemon.Launcher{Paths: appPaths(), Version: version.Version, Args: []string{"daemon"}}
}

func deps() cmd.Deps {
	return cmd.Deps{
		Paths:     appPaths(),
		Version:   version.Version,
		NewClient: newClient,
		EnsureCurrent: func(ctx context.Context) error {
			return launcher().EnsureCurrent(ctx, daemon.UpgradeTimeout)
		},
		EnsureCurrentIfRunning: func(ctx context.Context) error {
			return launcher().EnsureCurrentIfRunning(ctx)
		},
		ClaudePID:     claudePID,
		WindowAlive:   live,
		TerminalEvent: func(string) bool { return false },
		Serve:         serve,
		ChannelTools: func(ctx context.Context, session, scope string) ([]channel.Tool, string, string, error) {
			return interaction.ChannelTools(ctx, session, scope, claudePID(), version.Version)
		},
	}
}
