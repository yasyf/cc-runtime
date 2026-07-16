package runtime

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-runtime/mesh"
)

func meshStore() mesh.Store { return mesh.Store{Dir: appPaths().StateDir()} }

func sshRunner(target string) mesh.Runner { return mesh.SSHRunner{Target: target} }

// hostCmd manages the machine mesh: the peers this host reaches over ssh and
// how peers reach this host.
func hostCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "host",
		Short: "Manage the machine mesh of peers reachable over ssh",
	}
	c.AddCommand(hostAddCmd(), hostListCmd(), hostRemoveCmd())
	return c
}

func hostAddCmd() *cobra.Command {
	var noRecurse bool
	var self string
	c := &cobra.Command{
		Use:   "add <user@host>",
		Short: "Register a peer and cross-register this host on it over ssh",
		Long: "Add verifies the peer is reachable over ssh and has cc-runtime installed, records it in " +
			"the mesh, and — unless --no-recurse — shells `cc-runtime host add <self> --no-recurse` on the " +
			"peer so the registration is mutual. This host's ssh identity is detected from tailscale; pass " +
			"--self to override when tailscale is unavailable.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			target := args[0]
			out := c.OutOrStdout()
			step := func(msg string) { fmt.Fprintln(out, msg) }
			return meshStore().AddHost(c.Context(), mesh.LocalRunner{}, sshRunner(target), target, self, noRecurse, step)
		},
	}
	c.Flags().BoolVar(&noRecurse, "no-recurse", false, "skip cross-registering this host on the peer")
	c.Flags().StringVar(&self, "self", "", "this host's ssh target as peers reach it (defaults to the tailscale identity)")
	return c
}

func hostListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List mesh peers with a live reachability probe",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			reg, err := meshStore().Load()
			if err != nil {
				return err
			}
			return printHostList(c.Context(), c.OutOrStdout(), reg, sshRunner)
		},
	}
}

func hostRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <user@host>",
		Short: "Remove a peer from the mesh",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			target := args[0]
			if _, err := meshStore().Update(c.Context(), func(g *mesh.Registry) error {
				g.RemoveHost(target)
				return nil
			}); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "removed %s\n", target)
			return nil
		},
	}
}

// printHostList renders the registry: this host's self identity, then a table of
// peers with a live reachability/install column probed concurrently.
func printHostList(ctx context.Context, out io.Writer, reg *mesh.Registry, dial func(string) mesh.Runner) error {
	fmt.Fprintf(out, "self: %s\n", orDash(reg.Self))
	if len(reg.Hosts) == 0 {
		fmt.Fprintln(out, "no peers registered")
		return nil
	}
	results := mesh.VerifyAll(ctx, reg.Hosts, dial)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tNODE\tREACHABLE\tINSTALLED\tVERSION")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Target, mesh.HostNode(r.Target), yesNo(r.Reachable), yesNo(r.Installed), orDash(r.Version))
	}
	return tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
