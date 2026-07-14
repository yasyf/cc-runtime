package access

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strings"
	"time"
)

// tailscaleTimeout bounds the `tailscale status --json` shell-out at daemon
// boot and pair time.
const tailscaleTimeout = 5 * time.Second

// Tailscale describes a running tailscale node the daemon can serve HTTPS on:
// the MagicDNS FQDN (no trailing dot) certs are minted for, and the node's
// IPv4 the TLS listener binds.
type Tailscale struct {
	FQDN string
	IP   string
}

// DetectTailscale shells `tailscale status --json` and reports whether this
// host is a running tailscale node with a MagicDNS name and an IPv4. A missing
// binary, stopped/logged-out backend, or absent MagicDNS all report false —
// pairing then degrades to LAN-only.
func DetectTailscale(ctx context.Context) (Tailscale, bool) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return Tailscale{}, false
	}
	return parseTailscaleStatus(out)
}

// tailscaleStatus is the subset of `tailscale status --json` the detector reads.
type tailscaleStatus struct {
	BackendState string
	Self         struct {
		DNSName      string
		TailscaleIPs []string
	}
}

// parseTailscaleStatus extracts the FQDN and IPv4 from a status document,
// reporting false unless the backend is running with both present.
func parseTailscaleStatus(raw []byte) (Tailscale, bool) {
	var st tailscaleStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return Tailscale{}, false
	}
	if st.BackendState != "Running" || st.Self.DNSName == "" {
		return Tailscale{}, false
	}
	for _, s := range st.Self.TailscaleIPs {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			return Tailscale{FQDN: strings.TrimSuffix(st.Self.DNSName, "."), IP: s}, true
		}
	}
	return Tailscale{}, false
}
