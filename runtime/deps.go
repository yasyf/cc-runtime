package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/daemonrole"
	"github.com/yasyf/daemonkit/paths"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

func appPaths() paths.Paths { return interaction.AppPaths() }

func daemonRole() (daemonrole.Classifier, error) {
	rolePath, err := exec.LookPath("cc-runtime")
	if err != nil {
		return daemonrole.Classifier{}, fmt.Errorf("resolve cc-runtime role alias: %w", err)
	}
	rolePath, err = filepath.Abs(rolePath)
	if err != nil {
		return daemonrole.Classifier{}, fmt.Errorf("resolve absolute cc-runtime role alias: %w", err)
	}
	role := daemonrole.Classifier{RoleID: "com.yasyf.cc-runtime", RolePath: filepath.Clean(rolePath)}
	if err := role.Validate(); err != nil {
		return daemonrole.Classifier{}, err
	}
	return role, nil
}

func launcher() (daemon.Launcher, error) {
	role, err := daemonRole()
	if err != nil {
		return daemon.Launcher{}, err
	}
	return daemon.Launcher{
		Paths: appPaths(), Version: version.Version, LifecycleBuild: version.Version,
		Args: []string{"daemon"}, DaemonRole: role,
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
		ClaudePID:     claudePID,
		WindowAlive:   live,
		TerminalEvent: func(string) bool { return false },
		Serve:         serve,
		ChannelTools: func(ctx context.Context, session, scope string) ([]channel.Tool, string, string, error) {
			return interaction.ChannelTools(ctx, session, scope, claudePID(), version.Version)
		},
	}
}
