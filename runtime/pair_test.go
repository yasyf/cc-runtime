package runtime

import (
	"testing"

	"github.com/yasyf/cc-runtime/access"
)

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
