// Package mesh is cc-runtime's thin layer over synckit's shared host mesh: the
// interaction fan-out across peers (ListAll/PendingAll/AnswerRemote), the
// presence-routed delivery policy (Router), and the mesh.presence op a peer
// probes before routing here. Synckit owns host identity, transport, and
// presence. cc-runtime owns only its exact route policy state.
package mesh

import "github.com/yasyf/synckit/hostregistry"

// Binary is the cc-runtime CLI/daemon binary the mesh probes and invokes over
// ssh: a peer is "installed" when `command -v cc-runtime` resolves, the fan-out
// shells `cc-runtime rpc <op>`, and cross-registration shells `cc-runtime host
// add`. It is distinct from the shared mesh's own daemon binary (synckitd).
const Binary = "cc-runtime"

// Config is cc-runtime's handle to the single shared synckit host mesh.
var Config = hostregistry.Mesh
