//go:build lsp

package navigate

import (
	"context"
	"testing"
	"time"
)

// TestPool_StatsZeroValue is pure (no server): a fresh Pool reports all-zero
// counters and no live servers. Always runs — it needs no gopls.
func TestPool_StatsZeroValue(t *testing.T) {
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	s := pool.Stats()
	if s != (Stats{}) {
		t.Fatalf("fresh pool Stats = %+v, want all-zero", s)
	}
	if s.Live != 0 {
		t.Errorf("fresh pool Live = %d, want 0", s.Live)
	}
}

// TestPool_StatsSpawnAndQuery asserts the spawn/query counters track a single
// shared server and the dispatched query count. Mirrors the thundering-herd
// invariant (exactly one spawn) from the Stats angle. Needs gopls.
func TestPool_StatsSpawnAndQuery(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping stats spawn/query test")
	}
	root, shape, _ := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	warm(t, pool, root, shape) // first References that succeeds creates one server

	// Drive a few more queries against the same root.
	const extra = 3
	at := identPos(t, shape, "type Circle struct", "Circle")
	for range extra {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, err := pool.References(ctx, at, true); err != nil {
			cancel()
			t.Fatalf("References: %v", err)
		}
		cancel()
	}

	s := pool.Stats()
	if s.Spawns != 1 {
		t.Errorf("Spawns = %d, want exactly 1 (one shared server)", s.Spawns)
	}
	if s.Live != 1 {
		t.Errorf("Live = %d, want 1", s.Live)
	}
	// warm makes at least one successful query plus the extra ones; Queries
	// counts every dispatched file-routed call (including warm's failed retries).
	if s.Queries < int64(extra+1) {
		t.Errorf("Queries = %d, want >= %d", s.Queries, extra+1)
	}
}

// TestPool_StatsRestartAndEviction kills a pooled server and asserts the restart
// and eviction counters move when the next query transparently respawns it.
// Needs gopls.
func TestPool_StatsRestartAndEviction(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping stats restart test")
	}
	root, _, mainFile := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nav1, err := pool.Navigator(ctx, root)
	if err != nil {
		t.Fatalf("first Navigator: %v", err)
	}
	_ = nav1.cmd.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for nav1.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if nav1.Alive() {
		t.Fatal("server still Alive after Kill")
	}

	// A query now must restart-and-evict the dead server.
	at := identPos(t, mainFile, "Circle", "Circle")
	_ = queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return pool.Definition(ctx, at)
	})

	s := pool.Stats()
	if s.Restarts < 1 {
		t.Errorf("Restarts = %d, want >= 1", s.Restarts)
	}
	if s.Evictions < 1 {
		t.Errorf("Evictions = %d, want >= 1", s.Evictions)
	}
}
