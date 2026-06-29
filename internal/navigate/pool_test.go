//go:build lsp

package navigate

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests are the ADR 0017 Condition-2 evidence: the navigation layer stays
// correct and crash-free under many simultaneous parallel lanes. Run them with
// -race (the test-lsp Makefile target does). They skip when gopls is absent.

// TestPool_ConcurrentLanes drives many goroutines through one Pool, across two
// independent module roots, mixing definition / references / implementation
// queries — the parallel-lane workload Serena was chosen for. It asserts: no
// data race (under -race), no panic, exactly one server per root (no
// thundering-herd spawn), and correct cross-file results from every lane.
func TestPool_ConcurrentLanes(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping pool concurrency test")
	}
	rootA, shapeA, mainA := writeFixture(t)
	rootB, shapeB, mainB := writeFixture(t)

	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	// Warm both servers once so the first wave of lanes doesn't race purely on
	// gopls startup latency (correctness under churn is what we're probing, and
	// warming makes the per-query retries unnecessary).
	warm(t, pool, rootA, shapeA)
	warm(t, pool, rootB, shapeB)

	const lanes = 32
	var wg sync.WaitGroup
	var failures atomic.Int64
	wg.Add(lanes)
	for i := range lanes {
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Spread lanes across both roots and all three query kinds.
			shape, main := shapeA, mainA
			if i%2 == 1 {
				shape, main = shapeB, mainB
			}
			switch i % 3 {
			case 0:
				at := identPos(t, main, "Circle", "Circle") // main.go: shape.Circle{...}
				locs, err := pool.Definition(ctx, at)
				if err != nil || len(locs) == 0 || filepath.Base(locs[0].File) != "shape.go" {
					t.Logf("lane %d definition: err=%v locs=%v", i, err, locs)
					failures.Add(1)
				}
			case 1:
				at := identPos(t, shape, "type Circle struct", "Circle")
				locs, err := pool.References(ctx, at, true)
				if err != nil || len(locs) < 2 {
					t.Logf("lane %d references: err=%v n=%d", i, err, len(locs))
					failures.Add(1)
				}
			case 2:
				at := identPos(t, shape, "type Shape interface", "Shape")
				locs, err := pool.Implementations(ctx, at)
				if err != nil || len(locs) == 0 {
					t.Logf("lane %d implementations: err=%v n=%d", i, err, len(locs))
					failures.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()

	if n := failures.Load(); n > 0 {
		t.Fatalf("%d/%d concurrent lanes returned wrong/failed results", n, lanes)
	}

	// One server per root — the shared-not-per-lane invariant.
	pool.mu.Lock()
	got := len(pool.entries)
	pool.mu.Unlock()
	if got != 2 {
		t.Errorf("expected 2 pooled servers (one per root), got %d", got)
	}
}

// TestPool_ThunderingHerd starts many goroutines that all request the same root
// at once before it exists. Exactly one server must be created and shared.
func TestPool_ThunderingHerd(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping thundering-herd test")
	}
	root, _, _ := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	const callers = 24
	var wg sync.WaitGroup
	navs := make([]*LSP, callers)
	wg.Add(callers)
	start := make(chan struct{})
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			<-start // release all at once to maximize contention
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nav, err := pool.Navigator(ctx, root)
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			navs[i] = nav
		}(i)
	}
	close(start)
	wg.Wait()

	// Every caller must have received the identical *LSP instance.
	first := navs[0]
	if first == nil {
		t.Fatal("no server created")
	}
	for i, n := range navs {
		if n != first {
			t.Fatalf("caller %d got a different server instance; thundering herd spawned duplicates", i)
		}
	}
	pool.mu.Lock()
	got := len(pool.entries)
	pool.mu.Unlock()
	if got != 1 {
		t.Errorf("expected exactly 1 pooled server, got %d", got)
	}
}

// TestPool_RestartsDeadServer kills a pooled server out from under the Pool and
// asserts the next query transparently spawns a fresh one and still resolves —
// the lifecycle robustness Condition 2 requires under churn.
func TestPool_RestartsDeadServer(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH; skipping restart test")
	}
	root, _, mainFile := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })

	// Create the first server, then kill its process directly.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nav1, err := pool.Navigator(ctx, root)
	if err != nil {
		t.Fatalf("first Navigator: %v", err)
	}
	if nav1.cmd.Process == nil {
		t.Fatal("server has no process")
	}
	_ = nav1.cmd.Process.Kill()
	// Wait for the reader loop to observe the death.
	deadline := time.Now().Add(10 * time.Second)
	for nav1.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if nav1.Alive() {
		t.Fatal("server still reported Alive after Kill")
	}

	// Next query must route to a brand-new, working server.
	nav2, err := pool.Navigator(ctx, root)
	if err != nil {
		t.Fatalf("Navigator after crash: %v", err)
	}
	if nav2 == nav1 {
		t.Fatal("pool handed back the dead server instead of restarting")
	}
	at := identPos(t, mainFile, "Circle", "Circle")
	locs := queryWithRetry(t, func(ctx context.Context) ([]Location, error) {
		return pool.Definition(ctx, at)
	})
	if len(locs) == 0 || filepath.Base(locs[0].File) != "shape.go" {
		t.Fatalf("restarted server failed to resolve definition: %v", locs)
	}
}

// warm runs a definition query until the freshly-created server for root returns
// results, so concurrency tests probe contention rather than cold-start latency.
func warm(t *testing.T, pool *Pool, root, file string) {
	t.Helper()
	at := identPos(t, file, "type Circle struct", "Circle")
	for range 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		locs, err := pool.References(ctx, at, true)
		cancel()
		if err == nil && len(locs) > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("server for %s never warmed", root)
}
