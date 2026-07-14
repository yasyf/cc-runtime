package access

import (
	"fmt"
	"net"
)

// netIface is the subset of a network interface the LAN-IP picker reads, so a
// test drives it with fabricated data.
type netIface struct {
	Up       bool
	Loopback bool
	Addrs    []net.Addr
}

// pickLANIPs returns the usable LAN IPv4 addresses across ifaces: private-range
// (RFC 1918) addresses only, skipping down and loopback interfaces. A globally
// routable address is never a LAN leg — advertising one would carry the bearer
// token over cleartext HTTP beyond the local network.
func pickLANIPs(ifaces []netIface) []net.IP {
	var private []net.IP
	for _, ifc := range ifaces {
		if !ifc.Up || ifc.Loopback {
			continue
		}
		for _, a := range ifc.Addrs {
			v4 := addrIP(a).To4()
			if v4 != nil && v4.IsPrivate() {
				private = append(private, v4)
			}
		}
	}
	return private
}

// addrIP extracts the IP from a network address, ignoring the mask.
func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

// LANIPs picks the host's LAN IPv4 addresses from its real network interfaces.
func LANIPs() ([]net.IP, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	out := make([]netIface, 0, len(ifs))
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		out = append(out, netIface{
			Up:       ifc.Flags&net.FlagUp != 0,
			Loopback: ifc.Flags&net.FlagLoopback != 0,
			Addrs:    addrs,
		})
	}
	return pickLANIPs(out), nil
}
