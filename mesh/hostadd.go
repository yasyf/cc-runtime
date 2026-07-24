package mesh

import (
	"context"
	"encoding/json"
	"fmt"

	syncdaemon "github.com/yasyf/synckit/daemon"
	"github.com/yasyf/synckit/hostregistry"
	"github.com/yasyf/synckit/manifest"
)

// AddHost verifies target is reachable with cc-runtime installed, then delegates
// exact SSH fact registration, synckitd bootstrap, and inverse reconciliation to
// Synckit's canonical host-add workflow. self, when empty, is detected via
// tailscale; a stopped backend aborts before the shared registry changes.
func AddHost(ctx context.Context, r hostregistry.Runner, target, self string, noRecurse bool, onStep func(string)) error {
	if err := Initialize(ctx); err != nil {
		return err
	}
	step := func(msg string) {
		if onStep != nil {
			onStep(msg)
		}
	}

	if self == "" {
		if err := verifyTailscaleRunning(ctx, r); err != nil {
			return err
		}
	}

	if _, err := r.SSH(ctx, target, "true"); err != nil {
		return fmt.Errorf("%s is not reachable over ssh: %w", target, err)
	}
	step("reachable: " + target)

	if !Config.RemoteInstalledBinary(ctx, r, target, Binary) {
		return fmt.Errorf("%s is not installed on %s: install it there first, then re-run host add", Binary, target)
	}
	step(Binary + " installed on " + target)

	return syncdaemon.AddHost(ctx, r, []manifest.Manifest{{Name: "cc-runtime", Binary: Binary}}, target, self, noRecurse, onStep)
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
