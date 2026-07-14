// Package access is cc-runtime's local-first remote-access plane: the
// persisted bind + bearer-token state pairing flips, the LAN-IP picker and
// pairing payload the pair command prints, Bonjour advertising for LAN
// discovery, and the TLS listeners the daemon serves remote peers on — a
// pinned self-signed cert for the LAN, a tailscale-minted cert for the
// tailnet; remote traffic never rides cleartext HTTP. Nothing here talks to
// a relay — every path terminates at the local daemon.
package access

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Bind addresses the pair command flips between: BindLAN turns remote access
// on (the daemon serves the LAN/tailnet TLS listeners while the plain-HTTP
// plane stays on loopback); off returns to loopback only.
const (
	BindLAN      = "0.0.0.0"
	BindLoopback = "127.0.0.1"
)

// Store roots the access-plane state (config, token, TLS certs) in the app
// state dir (~/.cc-runtime).
type Store struct {
	Dir string
}

// Config is the persisted access-plane configuration. The zero value is the
// loopback-only default, so an absent file yields it.
type Config struct {
	// Bind is the persisted remote-access switch. Empty means loopback only;
	// "0.0.0.0" has the daemon serve the LAN/tailnet TLS listeners — the
	// plain-HTTP plane itself always binds loopback.
	Bind string `json:"bind,omitempty"`
}

// ConfigPath is the access config file.
func (s Store) ConfigPath() string { return filepath.Join(s.Dir, "access.json") }

// TokenPath is the bearer-token file every non-loopback request must present.
func (s Store) TokenPath() string { return filepath.Join(s.Dir, "token") }

// CertDir holds the tailscale-provisioned TLS certificate and key.
func (s Store) CertDir() string { return filepath.Join(s.Dir, "tls") }

// ReadConfig loads the access config. An absent file is the loopback-only zero
// value; a present-but-corrupt file fails loudly.
func (s Store) ReadConfig() (Config, error) {
	b, err := os.ReadFile(s.ConfigPath())
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read access config %q: %w", s.ConfigPath(), err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse access config %q: %w", s.ConfigPath(), err)
	}
	return c, nil
}

// WriteConfig persists the access config (0600), creating the state dir if
// needed.
func (s Store) WriteConfig(c Config) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.ConfigPath(), b, 0o600); err != nil {
		return fmt.Errorf("write access config %q: %w", s.ConfigPath(), err)
	}
	return nil
}

// ReadToken returns the bearer token, or "" when no token file exists (the
// daemon then refuses any non-loopback bind rather than serve unauthenticated).
func (s Store) ReadToken() (string, error) {
	b, err := os.ReadFile(s.TokenPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read token %q: %w", s.TokenPath(), err)
	}
	return strings.TrimSpace(string(b)), nil
}

// EnsureToken returns the bearer token, generating and persisting a fresh one
// the first time. It is idempotent and race-safe: the fresh token publishes via
// an exclusive link, so concurrent first runs all converge on the one token
// that actually landed on disk.
func (s Store) EnsureToken() (string, error) {
	tok, err := s.ReadToken()
	if err != nil {
		return "", err
	}
	if tok != "" {
		return tok, nil
	}
	fresh, tmp, err := s.mintTokenFile()
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, s.TokenPath()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return s.ReadToken()
		}
		return "", fmt.Errorf("publish token %q: %w", s.TokenPath(), err)
	}
	return fresh, nil
}

// ResetToken generates a fresh token, atomically replacing any existing one,
// and returns it. The daemon must be restarted to pick the new token up.
func (s Store) ResetToken() (string, error) {
	fresh, tmp, err := s.mintTokenFile()
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, s.TokenPath()); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("write token %q: %w", s.TokenPath(), err)
	}
	return fresh, nil
}

// mintTokenFile generates 32 crypto-random bytes as hex into a same-dir temp
// file (0600), creating the state dir if needed, and returns the token with
// the temp path for the caller to publish.
func (s Store) mintTokenFile() (token, path string, err error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(buf)
	f, err := os.CreateTemp(s.Dir, ".token-")
	if err != nil {
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	return tok, f.Name(), nil
}
