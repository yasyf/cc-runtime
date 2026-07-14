package access

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
)

// Fixed HTTPS ports, so pair payloads can name URLs before the daemon starts:
// TLSPort (0x6363, "cc") is the tailnet listener, LANTLSPort the LAN one.
const (
	TLSPort    = 25443
	LANTLSPort = 25444
)

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

// LANTLSListenerFactory returns a daemon ExtraHTTPListeners factory: a TLS
// listener on every interface at LANTLSPort serving the persisted self-signed
// LAN certificate, so the bearer token never crosses the LAN in cleartext.
// Clients authenticate the leg by pinning the pair payload's fingerprint.
func LANTLSListenerFactory(cert tls.Certificate) func(ctx context.Context) (net.Listener, error) {
	return func(ctx context.Context) (net.Listener, error) {
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(BindLAN, strconv.Itoa(LANTLSPort)))
		if err != nil {
			return nil, fmt.Errorf("bind LAN TLS listener: %w", err)
		}
		cfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		return tls.NewListener(ln, cfg), nil
	}
}
