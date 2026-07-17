// Package mesh is cc-runtime's thin layer over synckit's shared host mesh: the
// interaction fan-out across peers (ListAll/PendingAll/AnswerRemote), the
// presence-routed delivery policy (Router), and the mesh.presence op a peer
// probes before routing here. The host-identity registry, ssh transport, and
// console probe are synckit's (hostregistry, presence); the only state cc-runtime
// still owns is RouteOff — the presence-routing opt-out, which has no synckit
// equivalent — and it rides in the one shared state.json under a cc-runtime key
// synckit preserves byte-for-byte across its own writes.
package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/yasyf/synckit/hostregistry"
)

// Binary is the cc-runtime CLI/daemon binary the mesh probes and invokes over
// ssh: a peer is "installed" when `command -v cc-runtime` resolves, the fan-out
// shells `cc-runtime rpc <op>`, and cross-registration shells `cc-runtime host
// add`. It is distinct from the shared mesh's own daemon binary (synckitd).
const Binary = "cc-runtime"

// Config is cc-runtime's handle to the single shared synckit host mesh:
// ~/.config/synckit/{state.json,reconcile.lock}. Every synckit consumer reads
// and writes this one registry, so cc-runtime carries no parallel state file.
var Config = hostregistry.Mesh

// routeOffKey is cc-runtime's private key in the shared state.json holding the
// presence-routing opt-out. synckit's own writers preserve unknown keys
// byte-for-byte, so this cc-runtime-only flag survives alongside the shared
// self/hosts/addrs without a separate file.
const routeOffKey = "cc_runtime_route_off"

// LoadRouteOff reads cc-runtime's presence-routing opt-out from the shared
// state.json, returning false when the file or the key is absent. Like
// hostregistry's own Load it reads without the reconcile lock.
func LoadRouteOff() (bool, error) {
	path, err := Config.Path()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the shared mesh's own state file under the fixed config dir, not user-supplied.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read state %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse state %s: %w", path, err)
	}
	off := false
	if v, ok := raw[routeOffKey]; ok {
		if err := json.Unmarshal(v, &off); err != nil {
			return false, fmt.Errorf("parse %s: %w", routeOffKey, err)
		}
	}
	return off, nil
}

// SetRouteOff persists cc-runtime's presence-routing opt-out into the shared
// state.json under the reconcile lock, preserving every synckit-owned key
// byte-for-byte.
func SetRouteOff(ctx context.Context, off bool) error {
	return Config.UpdateRaw(ctx, func(raw map[string]json.RawMessage) error {
		b, err := json.Marshal(off)
		if err != nil {
			return fmt.Errorf("encode %s: %w", routeOffKey, err)
		}
		raw[routeOffKey] = b
		return nil
	})
}
