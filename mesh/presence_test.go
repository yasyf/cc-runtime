package mesh

import (
	"context"
	"os/user"
	"testing"
	"time"
)

// ioregPlist builds an ioreg-shaped XML plist for one console session with the
// given root-level locked flag, per-session on-console/locked flags, and user
// name.
func ioregPlist(rootLocked, onConsole, sessionLocked bool, userName string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>IOConsoleLocked</key>
	<` + boolTag(rootLocked) + `/>
	<key>IOConsoleUsers</key>
	<array>
		<dict>
			<key>CGSSessionScreenIsLocked</key>
			<` + boolTag(sessionLocked) + `/>
			<key>kCGSSessionOnConsoleKey</key>
			<` + boolTag(onConsole) + `/>
			<key>kCGSSessionUserIDKey</key>
			<integer>501</integer>
			<key>kCGSSessionUserNameKey</key>
			<string>` + userName + `</string>
		</dict>
	</array>
</dict>
</plist>
`
}

func boolTag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

const netstatHeaderLine = "Active Internet connections (including servers)\n" +
	"Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)      rxbytes  txbytes  rhiwat  shiwat  process:pid  options  gencnt  flags  flags1 usecnt rtncnt fltrs\n"

const netstatListen5900 = "tcp46      0      0  *.5900                 *.*                    LISTEN            0            0  131072  131072    screensharingd:912   00100 00000006 00000000000037c9 00000001 00000800      1      0 000000\n"

const netstatEstablished5900 = "tcp4       0      0  192.168.4.145.5900     192.168.4.50.54873     ESTABLISHED   14832         1083  131072  131600    screensharingd:912   00102 00000008 0000000001fec78d 00000081 04000900      2      0 000000\n"

func currentUser(t *testing.T) string {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	return me.Username
}

// darwinRunner scripts the two console probes: ioreg returns plist, netstat
// returns netstatOut.
func darwinRunner(plist, netstatOut string) *MockRunner {
	return NewMockRunner().On("ioreg", plist, nil).On("netstat", netstatOut, nil)
}

// TestDetectPresenceDarwinVerdicts drives DetectPresence over the darwin probe
// with an injected shell boundary: each row scripts ioreg and netstat, and the
// verdict must match the attended rule (this user, on-console, unlocked, not
// mirrored).
func TestDetectPresenceDarwinVerdicts(t *testing.T) {
	me := currentUser(t)
	tests := []struct {
		name         string
		plist        string
		netstat      string
		wantAttended bool
		wantReason   string
	}{
		{
			name:         "attended: this user, on console, unlocked, unmirrored",
			plist:        ioregPlist(false, true, false, me),
			netstat:      netstatHeaderLine + netstatListen5900,
			wantAttended: true,
		},
		{
			name:         "locked screen is unattended",
			plist:        ioregPlist(true, true, false, me),
			netstat:      netstatHeaderLine,
			wantAttended: false,
			wantReason:   "console screen is locked",
		},
		{
			name:         "another user via fast user switching is unattended",
			plist:        ioregPlist(false, true, false, me+"-other"),
			netstat:      netstatHeaderLine,
			wantAttended: false,
			wantReason:   "console owned by another user",
		},
		{
			name:         "headless box with no on-console session is unattended",
			plist:        ioregPlist(false, false, false, me),
			netstat:      netstatHeaderLine,
			wantAttended: false,
			wantReason:   "no GUI session on the console",
		},
		{
			name:         "inbound screen share on .5900 wins over an unlocked own console",
			plist:        ioregPlist(false, true, false, me),
			netstat:      netstatHeaderLine + netstatEstablished5900,
			wantAttended: false,
			wantReason:   "console screen is being shared",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := DetectPresence(context.Background(), osDarwin, darwinRunner(tc.plist, tc.netstat))
			if err != nil {
				t.Fatalf("DetectPresence: %v", err)
			}
			if p.Attended != tc.wantAttended {
				t.Fatalf("Attended = %v, want %v (%+v)", p.Attended, tc.wantAttended, p)
			}
			if p.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", p.Reason, tc.wantReason)
			}
		})
	}
}

// TestDetectPresenceNonDarwinDegrades proves every non-darwin platform reports
// unattended with a documented reason and never touches the shell boundary — a
// peer must not route to a machine whose attendance it cannot read.
func TestDetectPresenceNonDarwinDegrades(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			probed := NewMockRunner().Default("unexpected", nil)
			p, err := DetectPresence(context.Background(), goos, probed)
			if err != nil {
				t.Fatalf("DetectPresence: %v", err)
			}
			if p.Attended {
				t.Fatalf("Attended = true on %s, want false", goos)
			}
			if p.Reason != "presence detection unsupported on "+goos {
				t.Fatalf("Reason = %q, want the unsupported degradation", p.Reason)
			}
			if len(probed.Calls()) != 0 {
				t.Fatalf("non-darwin probed the shell boundary %d times, want 0", len(probed.Calls()))
			}
		})
	}
}

// TestDetectPresenceBoundsSlowProbe proves the console probe kills a wedged
// subprocess and fails fast rather than parking: with ioreg swapped for a sleep
// and a short probe timeout, DetectPresence returns an error bounded by the
// timeout.
func TestDetectPresenceBoundsSlowProbe(t *testing.T) {
	prev := ioregArgv
	ioregArgv = []string{"sleep", "30"}
	t.Cleanup(func() { ioregArgv = prev })

	prevTimeout := probeTimeout
	probeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prevTimeout })

	start := time.Now()
	_, err := DetectPresence(context.Background(), osDarwin, LocalRunner{})
	if err == nil {
		t.Fatal("DetectPresence returned nil, want the bounded subprocess to fail")
	}
	if elapsed := time.Since(start); elapsed > 5*probeTimeout {
		t.Fatalf("DetectPresence took %s, want it bounded by the %s probe timeout", elapsed, probeTimeout)
	}
}

// TestParseSessionMatchesIoregShape table-drives parseSession over the real ioreg
// plist shape: an unlocked console, a screen-locked one (via both flags), and a
// headless box.
func TestParseSessionMatchesIoregShape(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    sessionSnapshot
	}{
		{"live unlocked console", ioregPlist(false, true, false, "yasyf"), sessionSnapshot{OnConsole: true, ConsoleUser: "yasyf"}},
		{"locked via root flag", ioregPlist(true, true, false, "yasyf"), sessionSnapshot{OnConsole: true, Locked: true, ConsoleUser: "yasyf"}},
		{"locked via per-session flag", ioregPlist(false, true, true, "yasyf"), sessionSnapshot{OnConsole: true, Locked: true, ConsoleUser: "yasyf"}},
		{"headless: no on-console session", ioregPlist(false, false, false, "yasyf"), sessionSnapshot{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSession([]byte(tc.payload))
			if err != nil {
				t.Fatalf("parseSession: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseSession = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseScreenShare table-drives parseScreenShare over real `netstat -anv -p
// tcp` rows: only an inbound .5900 ESTABLISHED session counts; a header-less
// payload is malformed.
func TestParseScreenShare(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
		wantErr bool
	}{
		{"inbound established screen share on .5900", netstatHeaderLine + netstatListen5900 + netstatEstablished5900, true, false},
		{"listen-only on *.5900 is the idle listener", netstatHeaderLine + netstatListen5900, false, false},
		{"socketless table", netstatHeaderLine, false, false},
		{"malformed payload with no netstat header", "this is not netstat output\n", false, true},
		{"established .5900 row before any header is rejected", netstatEstablished5900, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseScreenShare([]byte(tc.payload))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseScreenShare = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScreenShare: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseScreenShare = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDetectPresenceIoregError proves a failed ioreg probe fails loud rather than
// reporting a bogus unattended snapshot.
func TestDetectPresenceIoregError(t *testing.T) {
	r := NewMockRunner().On("ioreg", "", context.DeadlineExceeded)
	if _, err := DetectPresence(context.Background(), osDarwin, r); err == nil {
		t.Fatal("DetectPresence returned nil, want the ioreg failure surfaced")
	}
}
