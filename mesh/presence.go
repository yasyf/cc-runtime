package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/yasyf/cc-interact/daemon"
)

// OpPresence reports this host's console attendance over the control socket, so a
// peer can ask `cc-runtime rpc mesh.presence` before routing an interaction here.
const OpPresence daemon.Op = "mesh.presence"

// osDarwin is the only GOOS the console probe supports; every other platform
// degrades to unattended with a documented reason.
const osDarwin = "darwin"

// probeTimeout bounds each presence probe subprocess so a wedged ioreg or netstat
// fails fast instead of parking the caller. It is a var so a test can shorten it.
var probeTimeout = 2 * time.Second

// ioregArgv reads the CoreGraphics console-session dictionary as an XML plist;
// screenShareArgv lists TCP sockets for an inbound Screen Sharing session. Vars so
// a test can point them at a fake.
var (
	ioregArgv       = []string{"ioreg", "-n", "Root", "-d1", "-a"}
	screenShareArgv = []string{"netstat", "-anv", "-p", "tcp"}
)

// Console-session plist keys, matching the macOS CoreGraphics session dictionary.
const (
	onConsoleKey    = "kCGSSessionOnConsoleKey"
	userNameKey     = "kCGSSessionUserNameKey"
	screenLockedKey = "CGSSessionScreenIsLocked"
)

// netstat -anv -p tcp vocabulary: Screen Sharing (VNC) serves on TCP 5900, so an
// inbound session shows this host's local address on that port in ESTABLISHED. The
// local address is field 3 and the state field 5 (0-indexed), under a "Proto"
// header.
const (
	screenSharePort  = ".5900"
	establishedState = "ESTABLISHED"
	netstatHeader    = "Proto"
	localAddrField   = 3
	stateField       = 5
)

// Presence is this host's console-attendance report: whether a real person is at
// the keyboard (Attended), and the raw console signals that decided it. Reason
// carries why an unattended host is unattended (a locked screen, a foreign user,
// an inbound screen share, no GUI session, or a platform with no console probe).
type Presence struct {
	Attended     bool   `json:"attended"`
	Reason       string `json:"reason,omitempty"`
	OnConsole    bool   `json:"on_console"`
	Locked       bool   `json:"locked"`
	ScreenShared bool   `json:"screen_shared"`
	ConsoleUser  string `json:"console_user,omitempty"`
}

// sessionSnapshot is a point-in-time read of this host's console GUI session.
type sessionSnapshot struct {
	OnConsole    bool
	Locked       bool
	ConsoleUser  string
	ScreenShared bool
}

// PresenceHandler answers OpPresence with this host's live console-attendance
// read, probed over a LocalRunner. Registered on the daemon so a peer reaches it
// through the rpc passthrough.
func PresenceHandler(hc daemon.HandlerCtx) daemon.Reply {
	p, err := DetectPresence(hc.Ctx, runtime.GOOS, LocalRunner{})
	if err != nil {
		return daemon.Reply{OK: false, Error: err.Error()}
	}
	body, _ := json.Marshal(p)
	return daemon.Reply{OK: true, Body: body}
}

// DetectPresence reports whether this host's console is attended. On darwin it
// probes the console session (ioreg) and any inbound screen share (netstat) over
// r; on every other platform it degrades to unattended with a documented reason —
// there is no console probe to fall back to, so a peer never routes an interaction
// to a machine whose attendance it cannot read.
func DetectPresence(ctx context.Context, goos string, r Runner) (Presence, error) {
	if goos != osDarwin {
		return Presence{Reason: fmt.Sprintf("presence detection unsupported on %s", goos)}, nil
	}
	snap, err := probeSession(ctx, r)
	if err != nil {
		return Presence{}, err
	}
	me, err := user.Current()
	if err != nil {
		return Presence{}, fmt.Errorf("resolve current user: %w", err)
	}
	p := Presence{
		OnConsole:    snap.OnConsole,
		Locked:       snap.Locked,
		ScreenShared: snap.ScreenShared,
		ConsoleUser:  snap.ConsoleUser,
	}
	p.Attended, p.Reason = attendedVerdict(snap, me.Username)
	return p, nil
}

// attendedVerdict decides whether snap is attended by this user, and why not when
// it is not: this user must own the console with an unlocked, unmirrored screen. A
// live screen share returns false — a mirror cannot prove a person is physically
// present at the keyboard.
func attendedVerdict(snap sessionSnapshot, me string) (bool, string) {
	switch {
	case !snap.OnConsole:
		return false, "no GUI session on the console"
	case snap.Locked:
		return false, "console screen is locked"
	case snap.ConsoleUser != me:
		return false, "console owned by another user"
	case snap.ScreenShared:
		return false, "console screen is being shared"
	default:
		return true, ""
	}
}

// probeSession reads the console session from ioreg and folds in an inbound
// screen-share probe from netstat, each under a bounded subprocess.
func probeSession(ctx context.Context, r Runner) (sessionSnapshot, error) {
	out, err := runProbe(ctx, r, ioregArgv)
	if err != nil {
		return sessionSnapshot{}, fmt.Errorf("run ioreg: %w", err)
	}
	snap, err := parseSession([]byte(out))
	if err != nil {
		return sessionSnapshot{}, err
	}
	nsout, err := runProbe(ctx, r, screenShareArgv)
	if err != nil {
		return sessionSnapshot{}, fmt.Errorf("run netstat: %w", err)
	}
	shared, err := parseScreenShare([]byte(nsout))
	if err != nil {
		return sessionSnapshot{}, err
	}
	snap.ScreenShared = shared
	return snap, nil
}

func runProbe(ctx context.Context, r Runner, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, _, err := r.Run(ctx, argv...)
	return out, err
}

// parseSession decodes an ioreg XML plist into a snapshot: the first on-console
// session decides; locked is the root flag OR that session's own screen-locked
// flag; an absent on-console session is headless.
func parseSession(payload []byte) (sessionSnapshot, error) {
	root, err := decodePlist(payload)
	if err != nil {
		return sessionSnapshot{}, err
	}
	dict, ok := root.(map[string]any)
	if !ok {
		return sessionSnapshot{}, fmt.Errorf("ioreg plist root is %T, want a dict", root)
	}
	users, _ := dict["IOConsoleUsers"].([]any)
	for _, raw := range users {
		sess, ok := raw.(map[string]any)
		if !ok || !asBool(sess[onConsoleKey]) {
			continue
		}
		return sessionSnapshot{
			OnConsole:   true,
			Locked:      asBool(dict["IOConsoleLocked"]) || asBool(sess[screenLockedKey]),
			ConsoleUser: asString(sess[userNameKey]),
		}, nil
	}
	return sessionSnapshot{}, nil
}

// parseScreenShare reports whether netstat shows an inbound Screen Sharing
// session: some socket's local address is on the VNC port .5900 in ESTABLISHED. A
// LISTEN row is the idle listener, so only ESTABLISHED counts; a payload with no
// netstat header is malformed and errors, mirroring parseSession's fail-loud on an
// invalid read.
func parseScreenShare(payload []byte) (bool, error) {
	sawHeader := false
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == netstatHeader {
			sawHeader = true
			continue
		}
		if !sawHeader {
			continue
		}
		if len(fields) <= stateField {
			continue
		}
		if strings.HasSuffix(fields[localAddrField], screenSharePort) && fields[stateField] == establishedState {
			return true, nil
		}
	}
	if !sawHeader {
		return false, fmt.Errorf("netstat output has no %q header: %d bytes", netstatHeader, len(payload))
	}
	return false, nil
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
