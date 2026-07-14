package runtime

import (
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/access"
)

func TestNeedsRestart(t *testing.T) {
	loopback := daemon.HTTPInfo{Port: 4100}
	remote := daemon.HTTPInfo{Port: 4100, Bind: access.BindLoopback, ExtraAddrs: []string{"0.0.0.0:25444"}}
	remoteTS := daemon.HTTPInfo{Port: 4100, Bind: access.BindLoopback, ExtraAddrs: []string{"0.0.0.0:25444", "100.64.0.9:25443"}}
	tests := []struct {
		name         string
		info         daemon.HTTPInfo
		tokenChanged bool
		wantExtras   int
		want         bool
	}{
		{"matching loopback daemon reused", loopback, false, 0, false},
		{"matching lan daemon reused", remote, false, 1, false},
		{"matching lan+tailnet daemon reused", remoteTS, false, 2, false},
		{"token change restarts", remote, true, 1, true},
		{"legacy cleartext lan bind restarts", daemon.HTTPInfo{Port: 4100, Bind: access.BindLAN, ExtraAddrs: []string{"0.0.0.0:25444"}}, false, 1, true},
		{"pairing a loopback daemon restarts", loopback, false, 1, true},
		{"tailscale now available but no tailnet leg restarts", remote, false, 2, true},
		{"tailscale gone but tailnet leg still up restarts", remoteTS, false, 1, true},
		{"pair off drops the tls legs", remote, false, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsRestart(tt.info, tt.tokenChanged, tt.wantExtras); got != tt.want {
				t.Errorf("needsRestart = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveBind(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", access.BindLoopback},
		{"0.0.0.0", "0.0.0.0"},
		{"127.0.0.1", "127.0.0.1"},
	}
	for _, tt := range tests {
		if got := effectiveBind(tt.in); got != tt.want {
			t.Errorf("effectiveBind(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSetBindRoundTrip(t *testing.T) {
	st := access.Store{Dir: t.TempDir()}
	if err := setBind(st, access.BindLAN); err != nil {
		t.Fatalf("setBind: %v", err)
	}
	cfg, err := st.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.Bind != access.BindLAN {
		t.Fatalf("Bind = %q, want %q", cfg.Bind, access.BindLAN)
	}

	// Idempotent when the bind already matches.
	if err := setBind(st, access.BindLAN); err != nil {
		t.Fatalf("setBind (match): %v", err)
	}

	if err := setBind(st, access.BindLoopback); err != nil {
		t.Fatalf("setBind (off): %v", err)
	}
	cfg, err = st.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.Bind != access.BindLoopback {
		t.Fatalf("Bind = %q, want %q", cfg.Bind, access.BindLoopback)
	}
}
