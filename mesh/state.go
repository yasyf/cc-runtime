package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/synckit/hostregistry"

	"github.com/yasyf/cc-runtime/interaction"
)

const (
	routeStateIdentity    = "cc-runtime-route-state-v1"
	routeStateDeclaration = "schema:{identity:string,version:uint64,fingerprint:string};route:{off:bool}"
	routeStateVersion     = 1
	routeLockDeadline     = 30 * time.Second
)

var routeStateFingerprint = hostregistry.SchemaFingerprint(routeStateIdentity, routeStateDeclaration)

type routeSchema struct {
	Identity    string `json:"identity"`
	Version     uint64 `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type routePayload struct {
	Off *bool `json:"off"`
}

type routeEnvelope struct {
	Schema routeSchema  `json:"schema"`
	Route  routePayload `json:"route"`
}

// Initialize creates the complete Synckit and cc-runtime mesh state when both
// are absent. Existing files must already match their exact v1 contracts.
func Initialize(ctx context.Context) error {
	if err := Config.InitializeState(ctx); err != nil {
		return fmt.Errorf("initialize shared mesh: %w", err)
	}
	return withRouteLock(ctx, func(path string) error {
		if _, err := os.Stat(path); err == nil {
			_, err = readRouteState(path)
			return err
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat route state %s: %w", path, err)
		}
		return writeRouteState(path, false)
	})
}

// LoadRouteOff reads the exact cc-runtime route policy state.
func LoadRouteOff() (bool, error) { return readRouteState(routeStatePath()) }

// SetRouteOff replaces the exact cc-runtime route policy state.
func SetRouteOff(ctx context.Context, off bool) error {
	return withRouteLock(ctx, func(path string) error {
		if _, err := readRouteState(path); err != nil {
			return err
		}
		return writeRouteState(path, off)
	})
}

func routeStatePath() string {
	return filepath.Join(interaction.AppPaths().StateDir(), "route.json")
}

func withRouteLock(ctx context.Context, fn func(string) error) (err error) {
	path := routeStatePath()
	lock, err := (proc.FileLockSpec{
		Path: path + ".lock", Mode: proc.FileLockExclusive, Deadline: routeLockDeadline,
	}).Acquire(ctx)
	if err != nil {
		return fmt.Errorf("lock route state %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return fn(path)
}

func readRouteState(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // fixed cc-runtime state path
	if err != nil {
		return false, fmt.Errorf("read route state %s: %w", path, err)
	}
	var envelope routeEnvelope
	if err := hostregistry.DecodeExactJSON(data, &envelope); err != nil {
		return false, fmt.Errorf("parse route state %s: %w", path, err)
	}
	if envelope.Schema.Identity != routeStateIdentity || envelope.Schema.Version != routeStateVersion || envelope.Schema.Fingerprint != routeStateFingerprint {
		return false, fmt.Errorf(
			"route state %s schema mismatch: got identity=%q version=%d fingerprint=%q",
			path, envelope.Schema.Identity, envelope.Schema.Version, envelope.Schema.Fingerprint,
		)
	}
	if envelope.Route.Off == nil {
		return false, fmt.Errorf("route state %s: route.off is required", path)
	}
	return *envelope.Route.Off, nil
}

func writeRouteState(path string, off bool) error {
	data, err := json.Marshal(routeEnvelope{
		Schema: routeSchema{Identity: routeStateIdentity, Version: routeStateVersion, Fingerprint: routeStateFingerprint},
		Route:  routePayload{Off: &off},
	})
	if err != nil {
		return fmt.Errorf("encode route state: %w", err)
	}
	if err := daemon.WriteFileDurable(path, data, 0o600); err != nil {
		return fmt.Errorf("write route state %s: %w", path, err)
	}
	return nil
}
