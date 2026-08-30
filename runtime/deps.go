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

func launcher() (daemon.Launcher, error) {
	spec, err := interaction.DaemonSpec()
	if err != nil {
		return daemon.Launcher{}, err
	}
	return daemon.Launcher{Daemon: spec, Paths: appPaths(), RuntimeBuild: version.Version}, nil
}

func newClient(context.Context) (*daemon.Client, error) {
	launcher, err := launcher()
	if err != nil {
		return nil, err
	}
	return launcher.NewClient()
}

func deps() cmd.Deps {
	return cmd.Deps{
		Paths:     appPaths(),
		Version:   version.Version,
		NewClient: newClient,
		EnsureCurrent: func(ctx context.Context) error {
			launcher, err := launcher()
			if err != nil {
				return err
			}
			return launcher.EnsureCurrent(ctx, daemon.UpgradeTimeout)
		},
		EnsureCurrentIfRunning: func(ctx context.Context) error {
			launcher, err := launcher()
			if err != nil {
				return err
			}
			return launcher.EnsureCurrentIfRunning(ctx)
		},
		Stop: func(ctx context.Context) error {
			launcher, err := launcher()
			if err != nil {
				return err
			}
			return launcher.Stop(ctx, daemon.UpgradeTimeout)
		},
		ClaudePID:     claudePID,
		WindowAlive:   live,
		TerminalEvent: func(string) bool { return false },
		Serve:         serve,
		ChannelTools: func(ctx context.Context, session, scope string) ([]channel.Tool, string, string, error) {
			return interaction.ChannelTools(ctx, session, scope, claudePID())
		},
	}
}
