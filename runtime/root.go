package runtime

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"

	"github.com/yasyf/cc-runtime/tui"
	"github.com/yasyf/cc-runtime/version"
)

func Root() *cobra.Command {
	d := deps()
	root := &cobra.Command{
		Use:           "cc-runtime",
		Short:         "Richer, persistent, remotely-answerable Claude Code harness tools",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		versionCmd(),
		cmd.DaemonCmd(d),
		cmd.WatchCmd(d),
		cmd.StatusCmd(d),
		cmd.StopCmd(d),
		cmd.SessionRecordCmd(d),
		cmd.GuardEditCmd(d),
		cmd.ChannelAckCmd(d),
		cmd.ChannelCmd(d),
		CaptureNotificationCmd(d),
		CaptureAskCmd(d),
		AnswerCmd(d),
		startCmd(d),
		pairCmd(d),
		WrapCmd(),
		tui.TUICmd(d),
	)
	return root
}

// startCmd is the human surface: it cold-starts the daemon and points the human
// at the cc-runtime answer surface (the TUI).
func startCmd(d cmd.Deps) *cobra.Command {
	var cwd string
	c := &cobra.Command{
		Use:   "start",
		Short: "Start the cc-runtime daemon",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if err := d.EnsureCurrent(ctx); err != nil {
				return err
			}
			scope := mustCwd(cwd)
			out := c.OutOrStdout()
			fmt.Fprintf(out, "scope:  %s\n", scope)
			fmt.Fprintln(out, "answer: cc-runtime tui")
			return nil
		},
	}
	c.Flags().StringVar(&cwd, "cwd", "", "working directory / scope (defaults to the current directory)")
	return c
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cc-runtime version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version.Version)
			return nil
		},
	}
}
