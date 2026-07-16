// Package mesh is cc-runtime's machine-mesh plane: the persisted host-identity
// registry (how peers reach this machine and the peers it reaches), the local
// and ssh command runners the mesh drives, tailscale self-detection, and the
// host-add bootstrap that cross-registers two machines over ssh. State lives in
// mesh.json under the app state dir, guarded by a cross-process flock so
// concurrent writers serialize.
package mesh

import "strings"

// Binary is the CLI/daemon binary the mesh probes and invokes over ssh: a peer
// is "installed" when `command -v cc-runtime` resolves, and cross-registration
// shells `cc-runtime host add`.
const Binary = "cc-runtime"

// Registry is the persisted host identity: Self is this machine's ssh target as
// peers reach it, Hosts are the peers this machine reaches.
type Registry struct {
	Self  string   `json:"self"`
	Hosts []string `json:"hosts"`
}

// UpsertHost adds a peer ssh target unless it is already registered.
func (g *Registry) UpsertHost(target string) {
	for _, h := range g.Hosts {
		if h == target {
			return
		}
	}
	g.Hosts = append(g.Hosts, target)
}

// RemoveHost drops a peer ssh target.
func (g *Registry) RemoveHost(target string) {
	kept := make([]string, 0, len(g.Hosts))
	for _, h := range g.Hosts {
		if h != target {
			kept = append(kept, h)
		}
	}
	g.Hosts = kept
}

// HostNode is an ssh target's short node label for display: the user is dropped
// and the host is cut at its first dot. "u@mac.tail.ts.net" and "mac.local"
// both yield "mac".
func HostNode(target string) string {
	host := target
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	label, _, _ := strings.Cut(host, ".")
	return label
}
