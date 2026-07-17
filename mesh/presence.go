package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/synckit/presence"
)

// OpPresence reports this host's console attendance over the control socket, so a
// peer can ask `cc-runtime rpc mesh.presence` before routing an interaction here.
const OpPresence daemon.Op = "mesh.presence"

// osDarwin is the only GOOS with a console probe; every other platform degrades
// to unattended with a documented reason so a peer never routes to a machine
// whose attendance it cannot read.
const osDarwin = "darwin"

// Presence is this host's console-attendance report: whether a real person is at
// the keyboard (Attended), the raw console snapshot that decided it, and, when
// unattended, why (Reason).
type Presence struct {
	presence.SessionSnapshot
	Attended bool   `json:"attended"`
	Reason   string `json:"reason,omitempty"`
}

// PresenceHandler answers OpPresence with this host's live console-attendance
// read. Registered on the daemon so a peer reaches it through the rpc passthrough.
func PresenceHandler(hc daemon.HandlerCtx) daemon.Reply {
	p, err := DetectPresence(hc.Ctx)
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(p)
	return daemon.Reply{OK: true, Body: body}
}

// DetectPresence reports whether this host's console is attended. On darwin it
// reads the console session (ioreg) and any inbound screen share (netstat) via
// synckit's presence probe; every other platform degrades to unattended with a
// documented reason.
func DetectPresence(ctx context.Context) (Presence, error) {
	if runtime.GOOS != osDarwin {
		return Presence{Reason: fmt.Sprintf("presence detection unsupported on %s", runtime.GOOS)}, nil
	}
	snap, err := presence.Session(ctx)
	if err != nil {
		return Presence{}, err
	}
	attended, err := presence.Attended(snap)
	if err != nil {
		return Presence{}, err
	}
	p := Presence{SessionSnapshot: snap, Attended: attended}
	if !attended {
		p.Reason = unattendedReason(snap)
	}
	return p, nil
}

// unattendedReason names why an on-darwin snapshot is not attended, in the same
// precedence synckit's Attended check applies.
func unattendedReason(snap presence.SessionSnapshot) string {
	switch {
	case !snap.OnConsole:
		return "no GUI session on the console"
	case snap.Locked:
		return "console screen is locked"
	case snap.ScreenShared:
		return "console screen is being shared"
	default:
		return "console owned by another user"
	}
}
