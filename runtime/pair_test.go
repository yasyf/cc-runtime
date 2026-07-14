package runtime

import (
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/access"
)

func TestNeedsRestart(t *testing.T) {
	lan := daemon.HTTPInfo{Port: 4100, Bind: access.BindLAN}
	lanTLS := daemon.HTTPInfo{Port: 4100, Bind: access.BindLAN, ExtraAddrs: []string{"100.64.0.9:25443"}}
	tests := []struct {
		name         string
		info         daemon.HTTPInfo
		desiredBind  string
		tokenChanged bool
		wantTLS      bool
		want         bool
	}{
		{"matching lan daemon reused", lan, access.BindLAN, false, false, false},
		{"matching lan+tls daemon reused", lanTLS, access.BindLAN, false, true, false},
		{"token change restarts", lan, access.BindLAN, true, false, true},
		{"bind mismatch restarts", daemon.HTTPInfo{Port: 4100, Bind: access.BindLoopback}, access.BindLAN, false, false, true},
		{"empty bind is loopback", daemon.HTTPInfo{Port: 4100}, access.BindLoopback, false, false, false},
		{"tailscale now available but no tls leg restarts", lan, access.BindLAN, false, true, true},
		{"tailscale gone but tls leg still up restarts", lanTLS, access.BindLAN, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsRestart(tt.info, tt.desiredBind, tt.tokenChanged, tt.wantTLS); got != tt.want {
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
