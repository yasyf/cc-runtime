package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// tailscaleStatus is the subset of `tailscale status --json` DetectSelf reads.
type tailscaleStatus struct {
	BackendState string
	Self         struct {
		DNSName string
	}
}

// DetectSelf returns the ssh target by which a peer reaches this machine,
// derived from the tailscale MagicDNS name and the local user
// ("user@host.tailnet.ts.net"). It errors when tailscale is absent, the backend
// is not Running, or MagicDNS is off — the caller then passes --self to override.
func DetectSelf(ctx context.Context, r Runner) (string, error) {
	out, _, err := r.Run(ctx, "tailscale", "status", "--json")
	if err != nil {
		return "", fmt.Errorf("detect self via tailscale (pass --self to override): %w", err)
	}
	var status tailscaleStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return "", fmt.Errorf("parse tailscale status (pass --self to override): %w", err)
	}
	if status.BackendState != "Running" {
		return "", fmt.Errorf("tailscale backend is %q, not Running (pass --self to override)", status.BackendState)
	}
	host := strings.TrimSuffix(status.Self.DNSName, ".")
	if host == "" {
		return "", errors.New("tailscale has no MagicDNS name (pass --self to override)")
	}
	user, _, err := r.Run(ctx, "id", "-un")
	if err != nil {
		return "", fmt.Errorf("detect local user: %w", err)
	}
	return strings.TrimSpace(user) + "@" + host, nil
}
