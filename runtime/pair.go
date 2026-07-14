package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/paths"

	"github.com/yasyf/cc-runtime/access"
)

// pairCmd exposes the daemon beyond loopback and prints a QR code a remote
// client scans to pair. It restarts the daemon when its bind or token must
// change.
func pairCmd(d cmd.Deps) *cobra.Command {
	var resetToken, off bool
	c := &cobra.Command{
		Use:   "pair",
		Short: "Expose the daemon to the LAN (and tailnet) and print a QR code to pair a client",
		Long: "Pair exposes the daemon over HTTPS only — the plain-HTTP plane stays on loopback. It " +
			"mints a bearer token plus a self-signed certificate served on every LAN interface, and " +
			"prints a QR code plus copyable JSON a remote cc-runtime client uses to connect; the " +
			"payload carries the certificate's SHA-256 fingerprint for the client to pin.\n\n" +
			"When tailscale is running with MagicDNS, the daemon also serves HTTPS on the tailscale " +
			"interface with a tailscale-minted certificate, and the payload carries that URL too.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if off {
				return runPairOff(c.Context(), d, c.OutOrStdout())
			}
			return runPair(c.Context(), d, c.OutOrStdout(), resetToken)
		},
	}
	c.Flags().BoolVar(&resetToken, "reset-token", false, "regenerate the bearer token before pairing")
	c.Flags().BoolVar(&off, "off", false, "disable remote access: rebind the daemon to loopback only")
	return c
}

func accessStore() access.Store { return access.Store{Dir: appPaths().StateDir()} }

// runPair turns remote access on, ensures a bearer token and the LAN cert,
// restarts the daemon if needed, then prints the reachable URLs, a QR code,
// and the pairing payload as copyable text. Every advertised URL is HTTPS —
// the LAN legs serve the self-signed cert whose fingerprint rides the payload.
func runPair(ctx context.Context, d cmd.Deps, out io.Writer, resetToken bool) error {
	st := accessStore()
	if err := setBind(st, access.BindLAN); err != nil {
		return err
	}
	prev, err := st.ReadToken()
	if err != nil {
		return err
	}
	var token string
	if resetToken {
		token, err = st.ResetToken()
	} else {
		token, err = st.EnsureToken()
	}
	if err != nil {
		return err
	}
	tokenChanged := resetToken || prev == ""

	lanCert, err := st.EnsureLANCert()
	if err != nil {
		return err
	}

	ts, tsOK := access.DetectTailscale(ctx)
	wantExtras := 1
	if tsOK {
		wantExtras = 2
	}
	// ensureDaemon fails unless the handshake shows every wanted TLS leg live,
	// so each advertised URL below has a listener serving it.
	if _, err := ensureDaemon(ctx, d, tokenChanged, wantExtras); err != nil {
		return err
	}

	ips, err := access.LANIPs()
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("no LAN IPv4 address found on any up, non-loopback interface")
	}

	urls := make([]string, 0, len(ips)+1)
	for _, ip := range ips {
		urls = append(urls, fmt.Sprintf("https://%s:%d", ip, access.LANTLSPort))
	}
	if tsOK {
		urls = append(urls, fmt.Sprintf("https://%s:%d", ts.FQDN, access.TLSPort))
	}

	host, err := os.Hostname()
	if err != nil {
		return err
	}
	_, payload, err := access.ComposePairPayload(host, urls, token, access.CertFingerprint(lanCert))
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "addresses:")
	for _, u := range urls {
		fmt.Fprintf(out, "  %s\n", u)
	}
	if !tsOK {
		fmt.Fprintln(out, "\ntailscale not detected (absent, logged out, or no MagicDNS); pairing is LAN-only")
	}
	fmt.Fprintln(out)
	qrterminal.GenerateWithConfig(payload, qrterminal.Config{
		Level:     qrterminal.M,
		Writer:    out,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Fprintln(out)
	fmt.Fprintf(out, "pair payload: %s\n", payload)
	return nil
}

// runPairOff drops the TLS legs and restarts the daemon, taking the plane off
// the LAN and stopping the Bonjour advertisement.
func runPairOff(ctx context.Context, d cmd.Deps, out io.Writer) error {
	if err := setBind(accessStore(), access.BindLoopback); err != nil {
		return err
	}
	if _, err := ensureDaemon(ctx, d, false, 0); err != nil {
		return err
	}
	fmt.Fprintln(out, "remote access disabled; the daemon now serves loopback only")
	return nil
}

// setBind persists the desired bind to the access config, a no-op when it
// already matches.
func setBind(st access.Store, bind string) error {
	cfg, err := st.ReadConfig()
	if err != nil {
		return err
	}
	if cfg.Bind == bind {
		return nil
	}
	cfg.Bind = bind
	return st.WriteConfig(cfg)
}

// ensureDaemon brings a current-version daemon serving wantExtras TLS legs
// online and returns its published handshake. EnsureCurrent cold-starts an
// absent daemon and upgrades a stale one; a live current daemon is then
// restarted when its token, bind, or extra-listener set no longer matches
// what pairing hands out.
func ensureDaemon(ctx context.Context, d cmd.Deps, tokenChanged bool, wantExtras int) (daemon.HTTPInfo, error) {
	if err := d.EnsureCurrent(ctx); err != nil {
		return daemon.HTTPInfo{}, err
	}
	info := readHTTPInfo(d.Paths)
	if needsRestart(info, tokenChanged, wantExtras) {
		if err := restartDaemon(ctx, d, d.NewClient()); err != nil {
			return daemon.HTTPInfo{}, err
		}
		info = readHTTPInfo(d.Paths)
	}
	if info.Port == 0 {
		return daemon.HTTPInfo{}, errors.New("daemon did not publish its HTTP port")
	}
	if len(info.ExtraAddrs) != wantExtras {
		return daemon.HTTPInfo{}, fmt.Errorf("daemon serves %d extra listeners, want %d", len(info.ExtraAddrs), wantExtras)
	}
	return info, nil
}

// needsRestart reports whether the running daemon no longer matches what
// pairing will advertise: the token changed, the plain-HTTP plane strayed off
// loopback (a legacy cleartext LAN bind), or the TLS leg count disagrees with
// the target.
func needsRestart(info daemon.HTTPInfo, tokenChanged bool, wantExtras int) bool {
	return tokenChanged || effectiveBind(info.Bind) != access.BindLoopback || len(info.ExtraAddrs) != wantExtras
}

// restartDaemon steps the running daemon down, waits for it to release the
// socket, then respawns one that re-reads the access config.
func restartDaemon(ctx context.Context, d cmd.Deps, client *daemon.Client) error {
	if _, err := client.Shutdown(); err != nil {
		return fmt.Errorf("shut down daemon: %w", err)
	}
	if !client.WaitGone(daemon.UpgradeTimeout) {
		return errors.New("daemon did not release the socket after shutdown")
	}
	return d.EnsureCurrent(ctx)
}

// effectiveBind resolves an empty bind to the loopback default the daemon
// applies, so it compares equal to a handshake that recorded "127.0.0.1".
func effectiveBind(bind string) string {
	if bind == "" {
		return access.BindLoopback
	}
	return bind
}

// readHTTPInfo reads the daemon's published handshake, returning the zero value
// when it is absent or unreadable.
func readHTTPInfo(p paths.Paths) daemon.HTTPInfo {
	b, err := os.ReadFile(p.HTTPInfoPath())
	if err != nil {
		return daemon.HTTPInfo{}
	}
	var info daemon.HTTPInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return daemon.HTTPInfo{}
	}
	return info
}
