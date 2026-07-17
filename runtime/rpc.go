package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/mesh"
)

const maxRPCStdinBytes = 1 << 20

// rpcAllowlist is the safe set of ops the low-level rpc passthrough may send to
// the local daemon. Control ops (guard-edit, shutdown, session-record, …) stay
// off the surface a peer reaches over ssh — rpc is not a blind proxy.
var rpcAllowlist = map[daemon.Op]bool{
	interaction.OpList:    true,
	interaction.OpPending: true,
	interaction.OpAnswer:  true,
	interaction.OpNotify:  true,
	mesh.OpPresence:       true,
}

// rpcCmd is the low-level RPC client: it sends one allowlisted op to the local
// daemon over its unix socket and prints the raw reply as one JSON line, exiting
// nonzero on an error reply. Peers drive it over ssh; the peer answers via its
// own loopback daemon.
func rpcCmd(d cmd.Deps) *cobra.Command {
	var jsonParams, cwd, session string
	var claudePID int
	c := &cobra.Command{
		Use:   "rpc <op>",
		Short: "Send one allowlisted op to the local daemon and print its raw reply",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			op := daemon.Op(args[0])
			if !rpcAllowlist[op] {
				return fmt.Errorf("op %q is not permitted over rpc", op)
			}
			// "-" reads the params from stdin so the payload never rides argv,
			// where any process on the machine could read it.
			var body json.RawMessage
			if jsonParams == "-" {
				b, err := io.ReadAll(io.LimitReader(c.InOrStdin(), maxRPCStdinBytes+1))
				if err != nil {
					return fmt.Errorf("read --json from stdin: %w", err)
				}
				if len(b) > maxRPCStdinBytes {
					return fmt.Errorf("--json from stdin exceeds %d bytes", maxRPCStdinBytes)
				}
				body = b
			} else if jsonParams != "" {
				body = json.RawMessage(jsonParams)
			}
			if len(body) > 0 && !json.Valid(body) {
				return fmt.Errorf("--json is not valid JSON: %s", body)
			}
			r, err := d.NewClient().Do(c.Context(), daemon.Envelope{
				Op:        op,
				Session:   session,
				ClaudePID: claudePID,
				Scope:     mustCwd(cwd),
				Body:      body,
			})
			if err != nil {
				return err
			}
			line, err := json.Marshal(r)
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), string(line))
			if !r.OK {
				return errors.New(r.Error)
			}
			return nil
		},
	}
	c.Flags().StringVar(&jsonParams, "json", "", `JSON params forwarded as the envelope body ("-" reads them from stdin)`)
	c.Flags().StringVar(&cwd, "cwd", "", "working directory / scope (defaults to the current directory)")
	c.Flags().StringVar(&session, "session", "", "session identity stamped on the envelope (subject resolution key)")
	c.Flags().IntVar(&claudePID, "claude-pid", 0, "window pid stamped on the envelope")
	return c
}
