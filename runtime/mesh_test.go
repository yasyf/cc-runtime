package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/yasyf/synckit/hostregistry"

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
	if err := mesh.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mesh: %v", err)
	}
	if _, err := mesh.Config.Update(context.Background(), func(g *hostregistry.Registry) error {
		g.Self = "me@here.tail.ts.net"
		return nil
	}); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	fact, err := hostregistry.NewSSHHostFact(target, "/opt/homebrew/bin/synckitd", nil)
	if err != nil {
		t.Fatalf("host fact: %v", err)
	}
	if err := mesh.Config.RegisterHost(context.Background(), fact); err != nil {
		t.Fatalf("register peer: %v", err)
	}
}

// isolateMesh points HOME (and clears XDG_CONFIG_HOME) at a fresh temp dir so the
// shared registry writes stay isolated to the test.
func isolateMesh(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", shortTempHome(t))
}

// TestMeshRouteStatusNoPeers proves status reports routing off for want of peers,
// distinct from the explicit opt-out.
func TestMeshRouteStatusNoPeers(t *testing.T) {
	isolateMesh(t)
	out, err := runMeshRoute(t, "status")
	if err != nil {
		t.Fatalf("route status: %v", err)
	}
	if !strings.Contains(out, "off (no peers registered)") {
		t.Fatalf("status = %q, want the no-peers notice", out)
	}
}

// TestMeshRouteToggle proves off/on persist RouteOff in product-owned exact
// state and status reflects it while the shared peer stays registered.
func TestMeshRouteToggle(t *testing.T) {
	isolateMesh(t)
	seedPeer(t, "u@peer.tail.ts.net")

	if out, err := runMeshRoute(t, "status"); err != nil || !strings.Contains(out, "on") {
		t.Fatalf("initial status = %q err=%v, want on", out, err)
	}

	if out, err := runMeshRoute(t, "off"); err != nil || !strings.Contains(out, "presence routing off") {
		t.Fatalf("route off = %q err=%v", out, err)
	}
	off, err := mesh.LoadRouteOff()
	if err != nil {
		t.Fatalf("load route off: %v", err)
	}
	if !off {
		t.Fatal("route off did not persist RouteOff")
	}
	reg, err := mesh.Config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
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
	off, _ = mesh.LoadRouteOff()
	if off {
		t.Fatal("route on did not clear RouteOff")
	}
}

// TestMeshRouteUnknownAction rejects an action that is neither on, off, nor status.
func TestMeshRouteUnknownAction(t *testing.T) {
	isolateMesh(t)
	if _, err := runMeshRoute(t, "toggle"); err == nil {
		t.Fatal("unknown action was not rejected")
	}
}
