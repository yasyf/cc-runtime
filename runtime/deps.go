package runtime

import (
	"context"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/paths"
	daemonversion "github.com/yasyf/daemonkit/version"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

func appPaths() paths.Paths { return interaction.AppPaths() }

func launcher(build string) (daemon.Launcher, error) {
	spec, err := interaction.DaemonSpec(build)
	if err != nil {
		return daemon.Launcher{}, err
	}
	return daemon.Launcher{Daemon: spec, Paths: appPaths(), RuntimeBuild: build}, nil
}

func newClient(build string) (*daemon.Client, error) {
	launcher, err := launcher(build)
	if err != nil {
		return nil, err
	}
	return launcher.NewClient()
}

func deps() cmd.Deps {
	build := daemonversion.Running(version.Version)
	return cmd.Deps{
		Paths:     appPaths(),
		Version:   build,
		NewClient: func(context.Context) (*daemon.Client, error) { return newClient(build) },
		EnsureCurrent: func(ctx context.Context) error {
			launcher, err := launcher(build)
			if err != nil {
				return err
			}
			return launcher.EnsureCurrent(ctx, daemon.UpgradeTimeout)
		},
		EnsureCurrentIfRunning: func(ctx context.Context) error {
			launcher, err := launcher(build)
			if err != nil {
				return err
			}
			return launcher.EnsureCurrentIfRunning(ctx)
		},
		Stop: func(ctx context.Context) error {
			launcher, err := launcher(build)
			if err != nil {
				return err
			}
			return launcher.Stop(ctx, daemon.UpgradeTimeout)
		},
		ClaudePID:     claudePID,
		WindowAlive:   live,
		TerminalEvent: func(string) bool { return false },
		Serve:         func(ctx context.Context) error { return serve(ctx, build) },
		ChannelTools: func(ctx context.Context, session, scope string) ([]channel.Tool, string, string, error) {
			return interaction.ChannelTools(ctx, build, session, scope, claudePID())
		},
	}
}
