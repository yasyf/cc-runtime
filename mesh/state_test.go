package mesh

import (
	"context"
	"os"
	"strings"
	"testing"
)

func isolateState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
}

func TestRouteStateExactV1RoundTrip(t *testing.T) {
	isolateState(t)
	if err := Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if off, err := LoadRouteOff(); err != nil || off {
		t.Fatalf("initial route off = %v, err=%v", off, err)
	}
	if err := SetRouteOff(context.Background(), true); err != nil {
		t.Fatalf("SetRouteOff: %v", err)
	}
	if off, err := LoadRouteOff(); err != nil || !off {
		t.Fatalf("persisted route off = %v, err=%v", off, err)
	}
}

func TestRouteStateRejectsLegacyShape(t *testing.T) {
	isolateState(t)
	if err := Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.WriteFile(routeStatePath(), []byte(`{"cc_runtime_route_off":true}`), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	if _, err := LoadRouteOff(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadRouteOff legacy error = %v, want exact-schema rejection", err)
	}
	if err := Initialize(context.Background()); err == nil {
		t.Fatal("Initialize repaired legacy state, want a hard failure")
	}
}

func TestRouteStateRejectsFingerprintMismatch(t *testing.T) {
	isolateState(t)
	if err := Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	data, err := os.ReadFile(routeStatePath())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	broken := strings.Replace(string(data), routeStateFingerprint, "wrong", 1)
	if err := os.WriteFile(routeStatePath(), []byte(broken), 0o600); err != nil {
		t.Fatalf("write bad fingerprint: %v", err)
	}
	if _, err := LoadRouteOff(); err == nil || !strings.Contains(err.Error(), "schema mismatch") {
		t.Fatalf("LoadRouteOff fingerprint error = %v", err)
	}
}

func TestSetRouteOffRequiresInitialization(t *testing.T) {
	isolateState(t)
	if err := SetRouteOff(context.Background(), true); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("SetRouteOff uninitialized error = %v", err)
	}
}
