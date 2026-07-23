package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-interact/cmd"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/access"
)

// apnsCmd configures the direct APNs lane: set points the daemon at an
// Apple-issued .p8 auth key, off disables the lane. Both restart a running
// daemon so it re-reads the config.
func apnsCmd(d cmd.Deps) *cobra.Command {
	c := &cobra.Command{
		Use:   "apns",
		Short: "Configure direct APNs delivery to registered iOS devices",
	}
	c.AddCommand(apnsSetCmd(d), apnsOffCmd(d))
	return c
}

func apnsSetCmd(d cmd.Deps) *cobra.Command {
	var key, keyID, teamID, bundleID string
	var sandbox bool
	c := &cobra.Command{
		Use:   "set",
		Short: "Enable APNs delivery with an Apple-issued auth key",
		Long: "Set enables direct APNs delivery: every question and notification fans out to the " +
			"device tokens registered via POST /api/push/device-tokens, authenticated by an ES256 " +
			"provider token minted from the .p8 auth key. The key stays where it is — only its " +
			"path is persisted.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			keyPath, err := filepath.Abs(key)
			if err != nil {
				return err
			}
			if _, err := access.LoadAPNSKey(keyPath); err != nil {
				return err
			}
			cfg := access.APNSConfig{KeyPath: keyPath, KeyID: keyID, TeamID: teamID, BundleID: bundleID, Sandbox: sandbox}
			return runAPNSSet(c.Context(), d, c.OutOrStdout(), cfg)
		},
	}
	c.Flags().StringVar(&key, "key", "", "path to the Apple-issued .p8 auth key")
	c.Flags().StringVar(&keyID, "key-id", "", "key ID the auth key was issued under")
	c.Flags().StringVar(&teamID, "team-id", "", "Apple developer team ID")
	c.Flags().StringVar(&bundleID, "bundle-id", "", "app bundle ID alerts are topic'd to")
	c.Flags().BoolVar(&sandbox, "sandbox", false, "deliver via the APNs sandbox environment")
	for _, flag := range []string{"key", "key-id", "team-id", "bundle-id"} {
		cobra.CheckErr(c.MarkFlagRequired(flag))
	}
	return c
}

func apnsOffCmd(d cmd.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "off",
		Short: "Disable APNs delivery",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := accessStore().ClearAPNSConfig(); err != nil {
				return err
			}
			if err := reloadDaemon(c.Context(), d); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "apns disabled")
			return nil
		},
	}
}

// runAPNSSet persists the APNs config and restarts a running daemon to pick
// it up.
func runAPNSSet(ctx context.Context, d cmd.Deps, out io.Writer, cfg access.APNSConfig) error {
	if err := accessStore().WriteAPNSConfig(cfg); err != nil {
		return err
	}
	if err := reloadDaemon(ctx, d); err != nil {
		return err
	}
	env := "production"
	if cfg.Sandbox {
		env = "sandbox"
	}
	fmt.Fprintf(out, "apns enabled: topic %s, key %s (%s)\n", cfg.BundleID, cfg.KeyID, env)
	return nil
}

// reloadDaemon restarts a running daemon so it re-reads the APNs config; when
// none is running the next start picks it up.
func reloadDaemon(ctx context.Context, d cmd.Deps) error {
	if err := d.EnsureCurrentIfRunning(ctx); errors.Is(err, daemon.ErrNoPeer) {
		return nil
	} else if err != nil {
		return err
	}
	return restartDaemon(ctx, d)
}
