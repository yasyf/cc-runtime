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

// stdinJSONFlag is --json's value: it records every value the flag is set to,
// so a literal anywhere on argv is refused even when a trailing `--json -`
// would win a scalar flag's last-one-wins parse. Set never errors — pflag
// wraps a Set error with the offending value, which is exactly the ps-visible
// literal the refusal exists to keep out of logs.
type stdinJSONFlag struct {
	values []string
}

func (f *stdinJSONFlag) String() string { return "" }

func (f *stdinJSONFlag) Set(v string) error {
	f.values = append(f.values, v)
	return nil
}

func (f *stdinJSONFlag) Type() string { return "string" }

// rpcCmd is the low-level RPC client: it sends one allowlisted op to the local
// daemon over its unix socket and prints the raw reply as one JSON line, exiting
// nonzero on an error reply. Peers drive it over ssh; the peer answers via its
// own loopback daemon.
func rpcCmd(d cmd.Deps) *cobra.Command {
	var jsonParams stdinJSONFlag
	var cwd, session string
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
			// Params only ride stdin (`--json -`), never ps-visible argv; every set
			// value is checked so a literal can't hide behind a trailing `--json -`.
			for _, v := range jsonParams.values {
				if v != "-" {
					return errors.New(`--json accepts only "-": params ride stdin, never argv`)
				}
			}
			var body json.RawMessage
			if len(jsonParams.values) > 0 {
				b, err := io.ReadAll(io.LimitReader(c.InOrStdin(), maxRPCStdinBytes+1))
				if err != nil {
					return fmt.Errorf("read --json from stdin: %w", err)
				}
				if len(b) > maxRPCStdinBytes {
					return fmt.Errorf("--json from stdin exceeds %d bytes", maxRPCStdinBytes)
				}
				body = b
			}
			// The payload can carry free-text answer content and rides error strings
			// over ssh into logs, so an invalid-JSON error reports only its length.
			if len(body) > 0 && !json.Valid(body) {
				return fmt.Errorf("--json is not valid JSON (%d bytes)", len(body))
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
	c.Flags().Var(&jsonParams, "json", `"-" reads the JSON envelope body from stdin (the only accepted value: params never ride argv)`)
	c.Flags().StringVar(&cwd, "cwd", "", "working directory / scope (defaults to the current directory)")
	c.Flags().StringVar(&session, "session", "", "session identity stamped on the envelope (subject resolution key)")
	c.Flags().IntVar(&claudePID, "claude-pid", 0, "window pid stamped on the envelope")
	return c
}
