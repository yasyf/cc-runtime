package access

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
)

// TLSPort is the fixed port the tailnet HTTPS listener binds (0x6363, "cc"),
// so pair payloads can name the URL before the daemon starts.
const TLSPort = 25443

// TLSListenerFactory returns a daemon ExtraHTTPListeners factory: a TLS
// listener bound to the tailscale interface only, serving certs from provider.
// Never front this with `tailscale serve` — its proxied requests arrive over
// loopback and would ride the daemon's loopback token bypass.
func TLSListenerFactory(ts Tailscale, provider *CertProvider) func(ctx context.Context) (net.Listener, error) {
	return func(ctx context.Context) (net.Listener, error) {
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(ts.IP, strconv.Itoa(TLSPort)))
		if err != nil {
			return nil, fmt.Errorf("bind tailscale TLS listener: %w", err)
		}
		cfg := &tls.Config{
			GetCertificate: provider.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		return tls.NewListener(ln, cfg), nil
	}
}
