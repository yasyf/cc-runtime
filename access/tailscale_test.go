package access

import "testing"

func TestParseTailscaleStatus(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Tailscale
		ok   bool
	}{
		{
			name: "running with magicdns and ipv4",
			raw: `{"BackendState":"Running","Self":{"DNSName":"yasyf-home.tail71af5d.ts.net.",` +
				`"TailscaleIPs":["100.88.252.58","fd7a:115c:a1e0::6d33:fc3c"]}}`,
			want: Tailscale{FQDN: "yasyf-home.tail71af5d.ts.net", IP: "100.88.252.58"},
			ok:   true,
		},
		{
			name: "ipv6 first, ipv4 picked",
			raw: `{"BackendState":"Running","Self":{"DNSName":"h.ts.net.",` +
				`"TailscaleIPs":["fd7a:115c:a1e0::1","100.64.0.9"]}}`,
			want: Tailscale{FQDN: "h.ts.net", IP: "100.64.0.9"},
			ok:   true,
		},
		{
			name: "logged out",
			raw:  `{"BackendState":"NeedsLogin","Self":{"DNSName":"","TailscaleIPs":[]}}`,
			ok:   false,
		},
		{
			name: "stopped backend",
			raw: `{"BackendState":"Stopped","Self":{"DNSName":"h.ts.net.",` +
				`"TailscaleIPs":["100.64.0.9"]}}`,
			ok: false,
		},
		{
			name: "no magicdns name",
			raw:  `{"BackendState":"Running","Self":{"DNSName":"","TailscaleIPs":["100.64.0.9"]}}`,
			ok:   false,
		},
		{
			name: "no ipv4",
			raw: `{"BackendState":"Running","Self":{"DNSName":"h.ts.net.",` +
				`"TailscaleIPs":["fd7a:115c:a1e0::1"]}}`,
			ok: false,
		},
		{
			name: "corrupt json",
			raw:  `not json`,
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTailscaleStatus([]byte(tt.raw))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("parseTailscaleStatus = %+v, want %+v", got, tt.want)
			}
		})
	}
}
