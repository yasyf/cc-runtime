package access

import (
	"net"
	"testing"
)

// ipNet wraps a parsed IP as a *net.IPNet, the concrete net.Addr the real
// net.Interface.Addrs returns, so the picker sees production-shaped input.
func ipNet(s string) net.Addr {
	return &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)}
}

func TestPickLANIPs(t *testing.T) {
	tests := []struct {
		name   string
		ifaces []netIface
		want   []string
	}{
		{
			name:   "private ipv4 included",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("192.168.1.5")}}},
			want:   []string{"192.168.1.5"},
		},
		{
			name:   "down interface skipped",
			ifaces: []netIface{{Up: false, Addrs: []net.Addr{ipNet("192.168.1.5")}}},
			want:   nil,
		},
		{
			name:   "loopback interface skipped",
			ifaces: []netIface{{Up: true, Loopback: true, Addrs: []net.Addr{ipNet("192.168.1.5")}}},
			want:   nil,
		},
		{
			name:   "ipv6 skipped",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("fe80::1"), ipNet("2001:db8::1")}}},
			want:   nil,
		},
		{
			name:   "link-local ipv4 skipped",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("169.254.10.1")}}},
			want:   nil,
		},
		{
			name:   "loopback ipv4 skipped",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("127.0.0.1")}}},
			want:   nil,
		},
		{
			name:   "public ipv4 excluded",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("8.8.8.8"), ipNet("10.0.0.2")}}},
			want:   []string{"10.0.0.2"},
		},
		{
			name:   "cgnat tailscale range excluded",
			ifaces: []netIface{{Up: true, Addrs: []net.Addr{ipNet("100.101.102.103")}}},
			want:   nil,
		},
		{
			name: "across interfaces, private only",
			ifaces: []netIface{
				{Up: true, Addrs: []net.Addr{ipNet("203.0.113.7")}},
				{Up: false, Addrs: []net.Addr{ipNet("192.168.9.9")}},
				{Up: true, Addrs: []net.Addr{ipNet("172.16.0.4")}},
			},
			want: []string{"172.16.0.4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickLANIPs(tt.ifaces)
			if len(got) != len(tt.want) {
				t.Fatalf("pickLANIPs = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i].String() != tt.want[i] {
					t.Fatalf("pickLANIPs = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
