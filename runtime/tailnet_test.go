package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/daemonkit/paths"
)

func testPaths(t *testing.T) paths.Paths {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := paths.Paths{App: ".cc-runtime-test"}
	if err := p.EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	return p
}

func writeHandshake(t *testing.T, p paths.Paths, info daemon.HTTPInfo) {
	t.Helper()
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.HTTPInfoPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTailnetListenersReusesTailnetPortNotLoopback proves the reuse hint comes
// from the previous tailnet extra listener, never the loopback plane's
// HTTPInfo.Port — the regression that flipped the tailnet port onto the
// loopback port across the first restart.
func TestTailnetListenersReusesTailnetPortNotLoopback(t *testing.T) {
	p := testPaths(t)
	addr := netip.MustParseAddr("127.0.0.1")

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hint := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	writeHandshake(t, p, daemon.HTTPInfo{
		Port:       hint + 1, // the loopback plane's port: must NOT become the hint
		ExtraAddrs: []string{netip.AddrPortFrom(addr, uint16(hint)).String()},
	})

	factories := tailnetListeners(p, "", []netip.Addr{addr})
	if len(factories) != 1 {
		t.Fatalf("len(factories) = %d, want 1", len(factories))
	}
	ln, err := factories[0](context.Background())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if got := ln.Addr().(*net.TCPAddr).Port; got != hint {
		t.Errorf("bound port = %d, want the previous tailnet port %d", got, hint)
	}
}

func TestTailnetListenersNilForNonLoopbackBind(t *testing.T) {
	p := testPaths(t)
	factories := tailnetListeners(p, "0.0.0.0", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	if factories != nil {
		t.Fatalf("factories = %d, want nil: a non-loopback bind already covers the tailnet", len(factories))
	}
}

func TestLastTailnetPort(t *testing.T) {
	tailnet := netip.MustParseAddr("100.64.1.2")
	tests := []struct {
		name  string
		setup func(t *testing.T, p paths.Paths)
		want  uint16
	}{
		{"absent handshake", func(*testing.T, paths.Paths) {}, 0},
		{"corrupt handshake", func(t *testing.T, p paths.Paths) {
			if err := os.WriteFile(filepath.Join(p.StateDir(), "http.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"loopback-only handshake ignores HTTPInfo.Port", func(t *testing.T, p paths.Paths) {
			writeHandshake(t, p, daemon.HTTPInfo{Port: 4321})
		}, 0},
		{"extra addr outside the tailnet set (LAN TLS leg)", func(t *testing.T, p paths.Paths) {
			writeHandshake(t, p, daemon.HTTPInfo{Port: 4321, ExtraAddrs: []string{"192.168.1.9:5555"}})
		}, 0},
		{"unparseable extra addr skipped", func(t *testing.T, p paths.Paths) {
			writeHandshake(t, p, daemon.HTTPInfo{ExtraAddrs: []string{"nonsense", "100.64.1.2:7777"}})
		}, 7777},
		{"tailnet extra addr wins over loopback port", func(t *testing.T, p paths.Paths) {
			writeHandshake(t, p, daemon.HTTPInfo{Port: 4321, ExtraAddrs: []string{"192.168.1.9:5555", "100.64.1.2:6666"}})
		}, 6666},
		{"v4-in-v6 extra addr matches unmapped", func(t *testing.T, p paths.Paths) {
			writeHandshake(t, p, daemon.HTTPInfo{ExtraAddrs: []string{"[::ffff:100.64.1.2]:8888"}})
		}, 8888},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testPaths(t)
			tt.setup(t, p)
			if got := lastTailnetPort(p, []netip.Addr{tailnet}); got != tt.want {
				t.Errorf("lastTailnetPort() = %d, want %d", got, tt.want)
			}
		})
	}
}
