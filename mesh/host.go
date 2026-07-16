package mesh

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// maxConcurrentHosts bounds the parallel verify fan-out.
const maxConcurrentHosts = 8

// AddHost registers target as a mesh peer and, unless noRecurse, cross-registers
// this host on it over ssh. It verifies target is reachable and has cc-runtime
// installed before persisting, then shells `cc-runtime host add <self>
// --no-recurse` on the peer. self, when empty, is detected via tailscale over
// local; a detection failure aborts, asking for an explicit --self. onStep (may
// be nil) reports each step as it happens.
func (s Store) AddHost(ctx context.Context, local, remote Runner, target, self string, noRecurse bool, onStep func(string)) error {
	step := func(msg string) {
		if onStep != nil {
			onStep(msg)
		}
	}

	if self == "" {
		detected, err := DetectSelf(ctx, local)
		if err != nil {
			return err
		}
		self = detected
	}

	if _, _, err := remote.Run(ctx, "true"); err != nil {
		return fmt.Errorf("%s is not reachable over ssh: %w", target, err)
	}
	step("reachable: " + target)

	if !remoteInstalled(ctx, remote) {
		return fmt.Errorf("%s is not installed on %s: install it there first, then re-run host add", Binary, target)
	}
	step(Binary + " installed on " + target)

	if _, err := s.Update(ctx, func(g *Registry) error {
		g.UpsertHost(target)
		g.Self = self
		return nil
	}); err != nil {
		return fmt.Errorf("save mesh after registering %s: %w", target, err)
	}
	step("registered " + target + " (self " + self + ")")

	if noRecurse {
		step("no-recurse: skipping inverse registration")
		return nil
	}

	if _, _, err := remote.Run(ctx, Binary, "host", "add", self, "--no-recurse"); err != nil {
		return fmt.Errorf("register inverse host on %s: %w", target, err)
	}
	step("registered inverse host " + self + " on " + target)
	return nil
}

// remoteInstalled reports whether cc-runtime is on the peer's PATH.
func remoteInstalled(ctx context.Context, remote Runner) bool {
	out, _, err := remote.Run(ctx, "command", "-v", Binary)
	return err == nil && strings.TrimSpace(out) != ""
}

// VerifyResult reports one host's live reachability and install state.
type VerifyResult struct {
	Target    string
	Reachable bool
	Installed bool
	Version   string
}

// Verify probes target over ssh: reachability (`ssh target true`), then whether
// cc-runtime is installed and its version.
func Verify(ctx context.Context, remote Runner, target string) VerifyResult {
	res := VerifyResult{Target: target}
	if _, _, err := remote.Run(ctx, "true"); err != nil {
		return res
	}
	res.Reachable = true
	if !remoteInstalled(ctx, remote) {
		return res
	}
	res.Installed = true
	if out, _, err := remote.Run(ctx, Binary, "version"); err == nil {
		res.Version = strings.TrimSpace(out)
	}
	return res
}

// VerifyAll verifies every host concurrently (bounded), returning one result per
// host in input order. dial builds the ssh runner bound to each host.
func VerifyAll(ctx context.Context, hosts []string, dial func(target string) Runner) []VerifyResult {
	results := make([]VerifyResult, len(hosts))
	sem := make(chan struct{}, maxConcurrentHosts)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, h string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = Verify(ctx, dial(h), h)
		}(i, h)
	}
	wg.Wait()
	return results
}
