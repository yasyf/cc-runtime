package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	stateFile      = "mesh.json"
	lockFile       = "mesh.lock"
	lockRetryDelay = 200 * time.Millisecond
)

// ErrLockBusy is returned when the mesh lock is held past the caller's deadline.
var ErrLockBusy = errors.New("mesh lock held by another process")

// Store roots the mesh registry in the app state dir (~/.cc-runtime), alongside
// the access-plane state.
type Store struct {
	Dir string
}

// Path is the mesh registry file.
func (s Store) Path() string { return filepath.Join(s.Dir, stateFile) }

// Load reads the registry, returning a zero Registry when the file is absent
// and failing loudly on a corrupt one.
func (s Store) Load() (*Registry, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mesh %q: %w", s.Path(), err)
	}
	var g Registry
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("parse mesh %q: %w", s.Path(), err)
	}
	return &g, nil
}

// Update runs fn against the freshly loaded registry under an exclusive
// cross-process flock, then persists the result atomically. The lock serializes
// read-modify-write across processes, so concurrent writers never lose updates.
func (s Store) Update(ctx context.Context, fn func(*Registry) error) (*Registry, error) {
	var out *Registry
	err := s.withLock(ctx, func() error {
		g, err := s.Load()
		if err != nil {
			return err
		}
		if err := fn(g); err != nil {
			return err
		}
		if err := s.save(g); err != nil {
			return err
		}
		out = g
		return nil
	})
	return out, err
}

// withLock runs fn while holding an exclusive flock on the lock file, giving up
// with ErrLockBusy once ctx is done so a contended acquire fails fast instead of
// blocking on a wedged holder.
func (s Store) withLock(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create mesh dir %q: %w", s.Dir, err)
	}
	lock := flock.New(filepath.Join(s.Dir, lockFile))
	locked, err := lock.TryLockContext(ctx, lockRetryDelay)
	if !locked {
		return fmt.Errorf("%w: %w", ErrLockBusy, err)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

// save writes the registry to mesh.json atomically: a temp file in the state dir
// renamed over the target.
func (s Store) save(g *Registry) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create mesh dir %q: %w", s.Dir, err)
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mesh: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(s.Dir, ".mesh-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp mesh: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp mesh: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp mesh: %w", err)
	}
	if err := os.Rename(tmpName, s.Path()); err != nil {
		return fmt.Errorf("rename mesh into place: %w", err)
	}
	return nil
}
