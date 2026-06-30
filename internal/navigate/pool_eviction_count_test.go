//go:build lsp

package navigate

import (
	"errors"
	"testing"
	"time"
)

// TestEvict_FailedCreationNotCounted is the regression for F-077: evict() is
// also called on a failed creation (NavigatorFor's `if err != nil { p.evict(k,
// ent) }` path), where ent.nav is nil — nothing was ever spawned. Counting that
// as an Evictions makes Spawns-Evictions an unreliable "live servers" signal.
func TestEvict_FailedCreationNotCounted(t *testing.T) {
	clk := time.Now()
	p := newTestPool(Config{}, &clk)
	t.Cleanup(func() { _ = p.Close() })

	ent := &poolEntry{ready: make(chan struct{}), err: errors.New("spawn failed")}
	close(ent.ready)
	p.entries["failed"] = ent

	p.evict("failed", ent)

	if got := p.Stats().Evictions; got != 0 {
		t.Errorf("Evictions after evicting a never-spawned (nav==nil) entry = %d, want 0", got)
	}
}

// TestEvict_SpawnedServerStillCounted guards the fix from over-firing: evicting
// an entry that DID spawn a real server (nav != nil) — e.g. a dead/errored
// cached server NavigatorFor is replacing — must still increment Evictions.
func TestEvict_SpawnedServerStillCounted(t *testing.T) {
	clk := time.Now()
	p := newTestPool(Config{}, &clk)
	t.Cleanup(func() { _ = p.Close() })

	// closed: true makes Close() short-circuit (see lsp.go) instead of driving
	// real RPC through a server that was never actually started.
	seedReady(p, "alive", &LSP{closed: true}, clk)
	ent := p.entries["alive"]

	p.evict("alive", ent)

	if got := p.Stats().Evictions; got != 1 {
		t.Errorf("Evictions after evicting a spawned server = %d, want 1", got)
	}
}
