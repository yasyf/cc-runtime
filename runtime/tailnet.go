package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/paths"
	"github.com/yasyf/synckit/meshtrust"
)

// tailnetListeners composes meshtrust.Listeners with the persisted handshake's
// port-reuse hint, so a restarting daemon reclaims its previous tailnet port.
func tailnetListeners(p paths.Paths, bind string, addrs []netip.Addr) []func(context.Context) (net.Listener, error) {
	return meshtrust.Listeners(bind, addrs, lastTailnetPort(p, addrs))
}

// lastTailnetPort reads the previous boot's handshake and returns the port its
// tailnet extra listener bound — matched by address against addrs, so the
// loopback plane's own port (HTTPInfo.Port) is never mistaken for the tailnet
// hint. Zero when the handshake is absent, unreadable, or has no tailnet leg.
func lastTailnetPort(p paths.Paths, addrs []netip.Addr) uint16 {
	b, err := os.ReadFile(p.HTTPInfoPath())
	if err != nil {
		return 0
	}
	var info daemon.HTTPInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return 0
	}
	for _, s := range info.ExtraAddrs {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ap.Addr().Unmap() == a.Unmap() {
				return ap.Port()
			}
		}
	}
	return 0
}
