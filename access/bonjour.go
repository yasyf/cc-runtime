package access

import (
	"context"
	"log/slog"
	"net"
	"os"

	"github.com/grandcat/zeroconf"
)

// BonjourService is the mDNS service type a LAN client browses to discover a
// paired cc-runtime daemon without a typed-in address.
const BonjourService = "_cc-runtime._tcp"

// BonjourHook returns the OnHTTPStart hook that advertises the HTTP plane over
// mDNS. A loopback bind is unreachable off-host, so it returns nil (no hook).
// The TXT records carry only the protocol version and host name — never the
// token.
func BonjourHook(bind string) func(ctx context.Context, port int) {
	if IsLoopbackBind(bind) {
		return nil
	}
	return func(ctx context.Context, port int) {
		host, err := os.Hostname()
		if err != nil {
			slog.Error("bonjour: resolve hostname", "err", err)
			return
		}
		server, err := zeroconf.Register(host, BonjourService, "local.", port,
			[]string{"v=1", "name=" + host}, nil)
		if err != nil {
			slog.Error("bonjour: register service", "service", BonjourService, "err", err)
			return
		}
		slog.Info("bonjour: advertising", "service", BonjourService, "instance", host, "port", port)
		<-ctx.Done()
		server.Shutdown()
	}
}

// IsLoopbackBind reports whether bind keeps the HTTP plane on loopback: empty
// (the loopback default) or a loopback IP. Anything else exposes the plane.
func IsLoopbackBind(bind string) bool {
	if bind == "" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}
