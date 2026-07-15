package access

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// apnsTokenRefresh re-mints the cached provider token at 40 minutes — inside
// Apple's 20–60 minute acceptance window with headroom on both sides.
const apnsTokenRefresh = 40 * time.Minute

// APNSConfig points the APNs lane at an Apple-issued auth key: the .p8 on
// disk (referenced in place, never copied), the key and team the provider
// token is minted under, and the app bundle alerts are topic'd to. The zero
// value is the disabled lane an absent config file yields.
type APNSConfig struct {
	KeyPath  string `json:"key_path"`
	KeyID    string `json:"key_id"`
	TeamID   string `json:"team_id"`
	BundleID string `json:"bundle_id"`
	Sandbox  bool   `json:"sandbox,omitempty"`
}

// Enabled reports whether the APNs lane is configured.
func (c APNSConfig) Enabled() bool { return c != (APNSConfig{}) }

// APNSConfigPath is the persisted APNs configuration file.
func (s Store) APNSConfigPath() string { return filepath.Join(s.Dir, "apns.json") }

// ReadAPNSConfig loads the APNs config. An absent file is the disabled-lane
// zero value; a present file missing any required field fails loudly — a
// configured lane never degrades silently.
func (s Store) ReadAPNSConfig() (APNSConfig, error) {
	b, err := os.ReadFile(s.APNSConfigPath())
	if errors.Is(err, fs.ErrNotExist) {
		return APNSConfig{}, nil
	}
	if err != nil {
		return APNSConfig{}, fmt.Errorf("read apns config %q: %w", s.APNSConfigPath(), err)
	}
	var c APNSConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return APNSConfig{}, fmt.Errorf("parse apns config %q: %w", s.APNSConfigPath(), err)
	}
	if c.KeyPath == "" || c.KeyID == "" || c.TeamID == "" || c.BundleID == "" {
		return APNSConfig{}, fmt.Errorf("apns config %q: key_path, key_id, team_id, and bundle_id are all required", s.APNSConfigPath())
	}
	return c, nil
}

// WriteAPNSConfig persists the APNs config (0600), creating the state dir if
// needed.
func (s Store) WriteAPNSConfig(c APNSConfig) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.APNSConfigPath(), b, 0o600); err != nil {
		return fmt.Errorf("write apns config %q: %w", s.APNSConfigPath(), err)
	}
	return nil
}

// ClearAPNSConfig disables the APNs lane by removing the config file.
// Clearing an unconfigured lane is a no-op.
func (s Store) ClearAPNSConfig() error {
	if err := os.Remove(s.APNSConfigPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove apns config %q: %w", s.APNSConfigPath(), err)
	}
	return nil
}

// LoadAPNSKey parses an Apple-issued APNs auth key: a PEM-encoded PKCS#8
// P-256 private key (.p8).
func LoadAPNSKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read apns key %q: %w", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("apns key %q: not a PEM-encoded PKCS#8 private key", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse apns key %q: %w", path, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("apns key %q: not a P-256 ECDSA key", path)
	}
	return key, nil
}

// apnsAuth mints and caches the ES256 provider token (kid header, iss=TeamID,
// iat) every APNs request authenticates with; callers supply the clock.
type apnsAuth struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string

	mu       sync.Mutex
	token    string
	mintedAt time.Time
}

// bearer returns the cached provider token, re-minting once it is
// apnsTokenRefresh old.
func (a *apnsAuth) bearer(now time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && now.Sub(a.mintedAt) < apnsTokenRefresh {
		return a.token, nil
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": a.teamID, "iat": now.Unix()})
	tok.Header["kid"] = a.keyID
	signed, err := tok.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("sign apns provider token: %w", err)
	}
	a.token, a.mintedAt = signed, now
	return signed, nil
}
