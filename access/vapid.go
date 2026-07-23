package access

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/yasyf/daemonkit/daemon"
)

// VAPIDKeys is the daemon's Web Push application-server keypair (RFC 8292).
// Public is what clients subscribe with; Private signs every push and never
// leaves the state dir.
type VAPIDKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

// VAPIDPath is the persisted VAPID keypair file.
func (s Store) VAPIDPath() string { return filepath.Join(s.Dir, "vapid.json") }

// EnsureVAPID returns the VAPID keypair, minting and persisting a fresh one
// (0600) the first time. It is idempotent: a second call returns the same keys.
func (s Store) EnsureVAPID() (VAPIDKeys, error) {
	b, err := os.ReadFile(s.VAPIDPath())
	if errors.Is(err, fs.ErrNotExist) {
		return s.mintVAPID()
	}
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("read vapid keys %q: %w", s.VAPIDPath(), err)
	}
	k, err := decodePersisted[VAPIDKeys](
		b,
		s.VAPIDPath(),
		vapidConfigSchemaIdentity,
		vapidConfigSchemaFingerprint,
	)
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("parse vapid keys %q: %w", s.VAPIDPath(), err)
	}
	if k.Public == "" || k.Private == "" {
		return VAPIDKeys{}, fmt.Errorf("vapid keys %q: missing key material", s.VAPIDPath())
	}
	return k, nil
}

// mintVAPID generates a fresh keypair and writes it to the vapid file (0600),
// creating the state dir if needed.
func (s Store) mintVAPID() (VAPIDKeys, error) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("generate vapid keys: %w", err)
	}
	k := VAPIDKeys{Public: public, Private: private}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return VAPIDKeys{}, err
	}
	b, err := encodePersisted(vapidConfigSchemaIdentity, vapidConfigSchemaFingerprint, k)
	if err != nil {
		return VAPIDKeys{}, err
	}
	if err := daemon.WriteFileDurable(s.VAPIDPath(), b, 0o600); err != nil {
		return VAPIDKeys{}, fmt.Errorf("write vapid keys %q: %w", s.VAPIDPath(), err)
	}
	return k, nil
}
