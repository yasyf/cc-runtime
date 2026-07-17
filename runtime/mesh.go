package runtime

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/mesh"
)

// meshCmd configures the machine mesh beyond its peer set: presence routing, the
// escape hatch that surfaces interactions on an attended peer when this host is
// unattended.
func meshCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "mesh",
		Short: "Configure the machine mesh",
	}
	c.AddCommand(meshRouteCmd())
	return c
}

// meshRouteCmd toggles and reports presence routing. Routing is on by default
// wherever peers exist; `off` persists the opt-out in the shared state without
// dropping the peers, and `on` clears it.
func meshRouteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "route <on|off|status>",
		Short: "Toggle surfacing interactions on an attended peer when this host is unattended",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			out := c.OutOrStdout()
			switch args[0] {
			case "on", "off":
				off := args[0] == "off"
				if err := mesh.SetRouteOff(c.Context(), off); err != nil {
					return err
				}
				fmt.Fprintf(out, "presence routing %s\n", args[0])
				return nil
			case "status":
				reg, err := mesh.Config.Load()
				if err != nil {
					return err
				}
				routeOff, err := mesh.LoadRouteOff()
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "presence routing: %s\n", routeStatus(reg, routeOff))
				return nil
			default:
				return fmt.Errorf("unknown route action %q (want on, off, or status)", args[0])
			}
		},
	}
	return c
}

// routeStatus describes routing for the mesh route status line: enabled, off via
// the escape hatch, or off for want of peers.
func routeStatus(reg *hostregistry.Registry, routeOff bool) string {
	switch {
	case len(reg.Hosts) == 0:
		return "off (no peers registered)"
	case routeOff:
		return "off"
	default:
		return "on"
	}
}
