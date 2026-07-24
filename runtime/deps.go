package runtime

import (
	"context"
	"os"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/paths"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/trust"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

func appPaths() paths.Paths { return interaction.AppPaths() }

func daemonAgent() (service.Agent, error) {
	executable, err := service.CanonicalExecutable()
	if err != nil {
		return service.Agent{}, err
	}
	return service.Agent{
		Label: "com.yasyf.cc-runtime", Program: executable, Args: []string{"daemon"},
		LogPath: appPaths().LogPath(), RestartPolicy: service.RestartOnFailure,
	}, nil
}

func daemonRoles() daemon.Roles {
	return daemon.Roles{
		Business: trust.UnprotectedRole, Lifecycle: "com.yasyf.cc-runtime.lifecycle.v1",
		StopControl: "com.yasyf.cc-runtime.stop.v1",
	}
}

func daemonTrustPolicy() (trust.TrustPolicy, error) {
	roles := daemonRoles()
	requirement := trust.Requirement{TeamID: "SXKCTF23Q2", SigningIdentifier: "cc-runtime"}
	return trust.NewTrustPolicy(trust.TrustPolicyConfig{
		ExpectedUID: os.Geteuid(), AllowUnprotected: true,
		Roles: map[trust.PeerRole]trust.Requirement{
			roles.Lifecycle: requirement, roles.StopControl: requirement,
		},
		StopRoles: []trust.PeerRole{roles.StopControl}, ReceiptRoles: []trust.PeerRole{roles.Lifecycle},
		ReadinessRoles: []trust.PeerRole{roles.Lifecycle},
	})
}

func launcher() (daemon.Launcher, error) {
	agent, err := daemonAgent()
	if err != nil {
		return daemon.Launcher{}, err
	}
	return daemon.Launcher{
		Paths: appPaths(), WireBuild: daemon.WireBuild, RuntimeBuild: version.Version,
		Agent: agent, Roles: daemonRoles(),
	}, nil
}

func newClient(ctx context.Context) (*daemon.Client, error) {
	launcher, err := launcher()
	if err != nil {
		return nil, err
	}
	return launcher.NewClient(ctx)
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
