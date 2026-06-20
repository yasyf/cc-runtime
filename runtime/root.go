package runtime

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-runtime/version"
)

func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "cc-runtime",
		Short:         "Richer, persistent, remotely-answerable Claude Code harness tools",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(versionCmd())
	return root
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
