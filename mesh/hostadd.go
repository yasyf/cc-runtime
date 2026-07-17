package mesh

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yasyf/synckit/hostregistry"
)

// AddHost registers target as a mesh peer in the shared registry and, unless
// noRecurse, cross-registers this host on it over ssh. It verifies target is
// reachable and has cc-runtime installed before persisting, then shells
// `cc-runtime host add <self> --no-recurse` on the peer. self, when empty, is
// detected via tailscale; a detection failure aborts, asking for --self. onStep
// (may be nil) reports each step as it happens.
func AddHost(ctx context.Context, r hostregistry.Runner, target, self string, noRecurse bool, onStep func(string)) error {
	step := func(msg string) {
		if onStep != nil {
			onStep(msg)
		}
	}

	if self == "" {
		if err := verifyTailscaleRunning(ctx, r); err != nil {
			return err
		}
		detected, err := hostregistry.DetectSelf(ctx, r)
		if err != nil {
			return err
		}
		self = detected
	}

	if _, err := r.SSH(ctx, target, "true"); err != nil {
		return fmt.Errorf("%s is not reachable over ssh: %w", target, err)
	}
	step("reachable: " + target)

	if !Config.RemoteInstalledBinary(ctx, r, target, Binary) {
		return fmt.Errorf("%s is not installed on %s: install it there first, then re-run host add", Binary, target)
	}
	step(Binary + " installed on " + target)

	if _, err := Config.Update(ctx, func(g *hostregistry.Registry) error {
		g.UpsertHost(target)
		g.Self = self
		return nil
	}); err != nil {
		return fmt.Errorf("save mesh after registering %s: %w", target, err)
	}
	step("registered " + target + " (self " + self + ")")

	if noRecurse {
		step("no-recurse: skipping inverse registration")
		return nil
	}

	if _, err := r.SSH(ctx, target, Binary+" host add "+hostregistry.ShellQuote(self)+" --no-recurse"); err != nil {
		return fmt.Errorf("register inverse host on %s: %w", target, err)
	}
	step("registered inverse host " + self + " on " + target)
	return nil
}

// verifyTailscaleRunning guards self-detection: synckit's DetectSelf reads the
// MagicDNS name regardless of backend state, and a Stopped tailscale can report
// a stale name — an unusable self identity that must abort before any mutation.
func verifyTailscaleRunning(ctx context.Context, r hostregistry.Runner) error {
	out, err := r.Local(ctx, "tailscale", "status", "--json")
	if err != nil {
		return fmt.Errorf("detect self via tailscale (pass --self to override): %w", err)
	}
	var status struct {
		BackendState string
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return fmt.Errorf("parse tailscale status (pass --self to override): %w", err)
	}
	if status.BackendState != "Running" {
		return fmt.Errorf("tailscale backend is %q, not Running (pass --self to override)", status.BackendState)
	}
	return nil
}
