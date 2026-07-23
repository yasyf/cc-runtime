package tui

import (
	"context"
	"testing"
	"time"

	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/mesh"
)

func TestWireMeshAllowsConcurrentScopedTUIs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := mesh.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mesh: %v", err)
	}
	if _, err := mesh.Config.Update(context.Background(), func(reg *hostregistry.Registry) error {
		reg.Self = meshSelf
		reg.Hosts = []string{meshHost}
		return nil
	}); err != nil {
		t.Fatalf("seed mesh: %v", err)
	}
	stateDir := t.TempDir()
	first := NewModel("/repo/first", nil, make(chan liveEvent))
	firstOwner, err := wireMesh(context.Background(), stateDir, &first)
	if err != nil {
		t.Fatalf("first wireMesh: %v", err)
	}
	t.Cleanup(func() { _ = firstOwner.Close(context.Background()) })

	secondCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second := NewModel("/repo/second", nil, make(chan liveEvent))
	secondOwner, err := wireMesh(secondCtx, stateDir, &second)
	if err != nil {
		t.Fatalf("second wireMesh: %v", err)
	}
	t.Cleanup(func() { _ = secondOwner.Close(context.Background()) })
	if first.scope == second.scope || first.local == nil || second.local == nil {
		t.Fatalf("concurrent scoped models not independently wired: first=%q second=%q", first.scope, second.scope)
	}
}
