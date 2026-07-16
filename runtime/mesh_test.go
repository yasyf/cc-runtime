package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/yasyf/cc-runtime/mesh"
)

// runMeshRoute drives the `mesh route` command with args, returning its stdout
// and any error.
func runMeshRoute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c := meshRouteCmd()
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

func seedPeer(t *testing.T, target string) {
	t.Helper()
	if _, err := meshStore().Update(context.Background(), func(g *mesh.Registry) error {
		g.Self = "me@here.tail.ts.net"
		g.UpsertHost(target)
		return nil
	}); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
}

// TestMeshRouteStatusNoPeers proves status reports routing off for want of peers,
// distinct from the explicit opt-out.
func TestMeshRouteStatusNoPeers(t *testing.T) {
	t.Setenv("HOME", shortTempHome(t))
	out, err := runMeshRoute(t, "status")
	if err != nil {
		t.Fatalf("route status: %v", err)
	}
	if !strings.Contains(out, "off (no peers registered)") {
		t.Fatalf("status = %q, want the no-peers notice", out)
	}
}

// TestMeshRouteToggle proves off/on persist RouteOff in mesh.json and status
// reflects it while the peer stays registered.
func TestMeshRouteToggle(t *testing.T) {
	t.Setenv("HOME", shortTempHome(t))
	seedPeer(t, "u@peer.tail.ts.net")

	if out, err := runMeshRoute(t, "status"); err != nil || !strings.Contains(out, "on") {
		t.Fatalf("initial status = %q err=%v, want on", out, err)
	}

	if out, err := runMeshRoute(t, "off"); err != nil || !strings.Contains(out, "presence routing off") {
		t.Fatalf("route off = %q err=%v", out, err)
	}
	reg, err := meshStore().Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reg.RouteOff {
		t.Fatal("route off did not persist RouteOff")
	}
	if len(reg.Hosts) != 1 {
		t.Fatalf("route off dropped peers: %+v", reg.Hosts)
	}
	if out, err := runMeshRoute(t, "status"); err != nil || !strings.Contains(out, "presence routing: off") {
		t.Fatalf("status after off = %q err=%v", out, err)
	}

	if out, err := runMeshRoute(t, "on"); err != nil || !strings.Contains(out, "presence routing on") {
		t.Fatalf("route on = %q err=%v", out, err)
	}
	reg, _ = meshStore().Load()
	if reg.RouteOff {
		t.Fatal("route on did not clear RouteOff")
	}
}

// TestMeshRouteUnknownAction rejects an action that is neither on, off, nor status.
func TestMeshRouteUnknownAction(t *testing.T) {
	t.Setenv("HOME", shortTempHome(t))
	if _, err := runMeshRoute(t, "toggle"); err == nil {
		t.Fatal("unknown action was not rejected")
	}
}
