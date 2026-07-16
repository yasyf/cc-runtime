package mesh

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"path/filepath"
)

func TestStoreLoadAbsentIsZero(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	g, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g.Self != "" || len(g.Hosts) != 0 {
		t.Fatalf("absent registry = %+v, want zero", g)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, err := s.Update(context.Background(), func(g *Registry) error {
		g.Self = "alice@mac.tail.ts.net"
		g.UpsertHost("bob@srv.tail.ts.net")
		g.UpsertHost("bob@srv.tail.ts.net") // dedup
		g.UpsertHost("carol@lap.tail.ts.net")
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	g, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g.Self != "alice@mac.tail.ts.net" {
		t.Fatalf("Self = %q", g.Self)
	}
	want := []string{"bob@srv.tail.ts.net", "carol@lap.tail.ts.net"}
	if fmt.Sprint(g.Hosts) != fmt.Sprint(want) {
		t.Fatalf("Hosts = %v, want %v", g.Hosts, want)
	}

	if _, err := s.Update(context.Background(), func(g *Registry) error {
		g.RemoveHost("bob@srv.tail.ts.net")
		return nil
	}); err != nil {
		t.Fatalf("Update remove: %v", err)
	}
	g, _ = s.Load()
	if fmt.Sprint(g.Hosts) != fmt.Sprint([]string{"carol@lap.tail.ts.net"}) {
		t.Fatalf("after remove Hosts = %v", g.Hosts)
	}
}

func TestStoreUpdateFnErrorAborts(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	sentinel := errors.New("boom")
	if _, err := s.Update(context.Background(), func(g *Registry) error {
		g.UpsertHost("x")
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update err = %v, want %v", err, sentinel)
	}
	g, _ := s.Load()
	if len(g.Hosts) != 0 {
		t.Fatalf("a failed Update persisted %v, want nothing", g.Hosts)
	}
}

// TestStoreConcurrentWriters proves the flock serializes read-modify-write:
// every one of N concurrent host appends lands with no lost updates.
func TestStoreConcurrentWriters(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Update(context.Background(), func(g *Registry) error {
				g.UpsertHost(fmt.Sprintf("host-%02d", i))
				return nil
			}); err != nil {
				t.Errorf("concurrent Update %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	g, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(g.Hosts) != n {
		t.Fatalf("landed %d hosts, want %d (lost updates): %v", len(g.Hosts), n, g.Hosts)
	}
	sort.Strings(g.Hosts)
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("host-%02d", i)
		if g.Hosts[i] != want {
			t.Fatalf("Hosts[%d] = %q, want %q", i, g.Hosts[i], want)
		}
	}
}

// TestStoreUpdateLockBusy asserts a contended acquire past the deadline fails
// with ErrLockBusy rather than blocking forever on a held lock.
func TestStoreUpdateLockBusy(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}

	held := flock.New(filepath.Join(dir, lockFile))
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("seed lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = held.Unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = s.Update(ctx, func(g *Registry) error {
		g.UpsertHost("never")
		return nil
	})
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("Update under a held lock = %v, want ErrLockBusy", err)
	}
}
