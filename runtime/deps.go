package runtime

import (
	"context"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/paths"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

func appPaths() paths.Paths { return interaction.AppPaths() }

func newClient() *daemon.Client { return daemon.NewClient(appPaths().SocketPath()) }

func launcher() daemon.Launcher {
	return daemon.Launcher{Paths: appPaths(), Version: version.Version, Args: []string{"daemon"}}
}

func deps() cmd.Deps {
	return cmd.Deps{
		Paths:                  appPaths(),
		Version:                version.Version,
		NewClient:              newClient,
		EnsureCurrent:          func(context.Context) error { return launcher().EnsureCurrent(daemon.UpgradeTimeout) },
		EnsureCurrentIfRunning: func() error { return launcher().EnsureCurrentIfRunning() },
		ClaudePID:              claudePID,
		WindowAlive:            live,
		TerminalEvent:          func(string) bool { return false },
		Serve:                  serve,
		ChannelTools:           interaction.ChannelTools,
	}
}
