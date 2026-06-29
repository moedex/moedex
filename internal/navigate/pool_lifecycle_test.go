//go:build lsp

package navigate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// virtClock is a thread-safe virtual clock for lifecycle tests: the Pool reads
// it (via cfg.now) from worker goroutines while the test advances it, so it must
// be race-free. Stored as unix-nanos in an atomic.Int64.
type virtClock struct{ ns atomic.Int64 }

func (c *virtClock) set(t time.Time) { c.ns.Store(t.UnixNano()) }
func (c *virtClock) get() time.Time  { return time.Unix(0, c.ns.Load()) }

// --- pure unit tests (always run; no gopls) --------------------------------

// TestBackoff_PureGrowth pins backoff()'s shape: attempt 0 == base, doubling
// before the cap, monotonic non-decreasing, and clamped at max. No randomness,
// no server — always runs and never flakes.
func TestBackoff_PureGrowth(t *testing.T) {
	base := 100 * time.Millisecond
	max := 1 * time.Second

	if got := backoff(0, base, max); got != base {
		t.Errorf("backoff(0) = %v, want base %v", got, base)
	}
	if got := backoff(1, base, max); got != 200*time.Millisecond {
		t.Errorf("backoff(1) = %v, want 200ms", got)
	}
	if got := backoff(2, base, max); got != 400*time.Millisecond {
		t.Errorf("backoff(2) = %v, want 400ms", got)
	}
	if got := backoff(3, base, max); got != 800*time.Millisecond {
		t.Errorf("backoff(3) = %v, want 800ms", got)
	}
	// Past the cap everything clamps.
	for n := 4; n < 20; n++ {
		if got := backoff(n, base, max); got != max {
			t.Errorf("backoff(%d) = %v, want capped %v", n, got, max)
		}
	}
	// Monotonic non-decreasing across the whole range.
	prev := time.Duration(-1)
	for n := 0; n < 20; n++ {
		got := backoff(n, base, max)
		if got < prev {
			t.Errorf("backoff not monotonic at %d: %v < %v", n, got, prev)
		}
		prev = got
	}
}

// TestBackoff_ZeroDefaults verifies zero base/max fall back to the package
// defaults rather than producing a zero or runaway delay.
func TestBackoff_ZeroDefaults(t *testing.T) {
	if got := backoff(0, 0, 0); got != defaultBaseBackoff {
		t.Errorf("backoff(0,0,0) = %v, want default base %v", got, defaultBaseBackoff)
	}
	if got := backoff(100, 0, 0); got != defaultMaxBackoff {
		t.Errorf("backoff(100,0,0) = %v, want default max %v", got, defaultMaxBackoff)
	}
}

// fakeServer returns a *LSP that the lifecycle map-manipulation tests can store
// and "Close" without a real process. The default-build stub *LSP has no fields;
// in the lsp build *LSP has fields but Close() is idempotent and safe on a
// zero-value-ish instance constructed here only for map bookkeeping — we never
// drive RPC through it. Alive() reads the atomic dead flag (false by default,
// reported as alive); these tests only assert map membership, never liveness.
func fakeServer() *LSP { return &LSP{} }

// newTestPool builds a Pool with a controllable virtual clock and pre-seeded
// entries, bypassing NewLSP entirely so map-lifecycle logic is tested without a
// language server.
func newTestPool(cfg Config, clk *time.Time) *Pool {
	cfg.now = func() time.Time { return *clk }
	p := NewPool(cfg)
	return p
}

// seedReady inserts a ready (creation-finished) entry whose nav is a fake server
// last used at lu.
func seedReady(p *Pool, key string, nav *LSP, lu time.Time) {
	ent := &poolEntry{ready: make(chan struct{}), nav: nav}
	close(ent.ready)
	ent.lastUsed.Store(lu.UnixNano())
	p.entries[key] = ent
}

// seedInFlight inserts an entry whose creation has NOT finished (ready open).
func seedInFlight(p *Pool, key string) {
	p.entries[key] = &poolEntry{ready: make(chan struct{})}
}

// TestSweepIdle_RemovesExpired drives virtual time past IdleTTL and asserts the
// expired ready entry is swept, a within-TTL entry survives, the requested key is
// spared, and an in-flight entry is never swept. No server, no real sleep.
func TestSweepIdle_RemovesExpired(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	ttl := 30 * time.Second
	p := newTestPool(Config{IdleTTL: ttl}, &clk)

	old := clk.Add(-ttl - time.Second) // well past TTL
	fresh := clk.Add(-time.Second)     // within TTL

	seedReady(p, "expired", fakeServer(), old)
	seedReady(p, "fresh", fakeServer(), fresh)
	seedReady(p, "requested", fakeServer(), old) // expired-by-time but is `except`
	seedInFlight(p, "inflight")                  // open ready: never swept

	p.mu.Lock()
	victims := p.sweepIdleLocked(clk, "requested")
	p.mu.Unlock()

	if len(victims) != 1 {
		t.Fatalf("expected exactly 1 victim, got %d", len(victims))
	}
	if _, ok := p.entries["expired"]; ok {
		t.Error("expired entry should have been swept")
	}
	if _, ok := p.entries["fresh"]; !ok {
		t.Error("fresh (within-TTL) entry must survive")
	}
	if _, ok := p.entries["requested"]; !ok {
		t.Error("currently-requested root must never be swept")
	}
	if _, ok := p.entries["inflight"]; !ok {
		t.Error("in-flight (ready-open) entry must never be swept")
	}
}

// TestSweepIdle_DisabledIsNoop confirms IdleTTL==0 sweeps nothing — the original
// behavior a zero Config must preserve.
func TestSweepIdle_DisabledIsNoop(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	p := newTestPool(Config{IdleTTL: 0}, &clk)
	seedReady(p, "ancient", fakeServer(), clk.Add(-time.Hour))

	p.mu.Lock()
	victims := p.sweepIdleLocked(clk, "")
	p.mu.Unlock()

	if len(victims) != 0 {
		t.Fatalf("IdleTTL=0 must sweep nothing, got %d victims", len(victims))
	}
	if _, ok := p.entries["ancient"]; !ok {
		t.Error("entry wrongly removed with TTL disabled")
	}
}

// TestPickLRU_EvictsOldest seeds MaxServers ready entries with distinct last-use
// times and asserts the smallest-lastUsed one is the LRU victim chosen for a new
// key, while in-flight entries are never eligible. Deterministic clock.
func TestPickLRU_EvictsOldest(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	p := newTestPool(Config{MaxServers: 3}, &clk)

	seedReady(p, "a", fakeServer(), clk.Add(-10*time.Second)) // oldest -> victim
	seedReady(p, "b", fakeServer(), clk.Add(-5*time.Second))
	seedReady(p, "c", fakeServer(), clk.Add(-1*time.Second))

	p.mu.Lock()
	victim := p.pickLRULocked("newkey")
	p.mu.Unlock()

	if victim == nil {
		t.Fatal("expected an LRU victim at capacity")
	}
	if _, ok := p.entries["a"]; ok {
		t.Error("oldest entry 'a' should have been evicted as LRU")
	}
	if _, ok := p.entries["b"]; !ok {
		t.Error("'b' must survive")
	}
	if _, ok := p.entries["c"]; !ok {
		t.Error("'c' must survive")
	}
}

// TestPickLRU_UnderCapNoEvict: below MaxServers, no eviction happens.
func TestPickLRU_UnderCapNoEvict(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	p := newTestPool(Config{MaxServers: 5}, &clk)
	seedReady(p, "a", fakeServer(), clk)
	seedReady(p, "b", fakeServer(), clk)

	p.mu.Lock()
	victim := p.pickLRULocked("newkey")
	p.mu.Unlock()

	if victim != nil {
		t.Error("no eviction expected below MaxServers")
	}
	if len(p.entries) != 2 {
		t.Errorf("map size = %d, want 2 (unchanged)", len(p.entries))
	}
}

// TestPickLRU_AllInFlightSoftOvershoot: at capacity but every peer is a cold
// creation -> no eligible victim -> soft overshoot (nil) rather than killing an
// in-flight creation or failing the lane.
func TestPickLRU_AllInFlightSoftOvershoot(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	p := newTestPool(Config{MaxServers: 2}, &clk)
	seedInFlight(p, "a")
	seedInFlight(p, "b")

	p.mu.Lock()
	victim := p.pickLRULocked("newkey")
	p.mu.Unlock()

	if victim != nil {
		t.Error("must not evict an in-flight creation; expected soft overshoot (nil)")
	}
	if len(p.entries) != 2 {
		t.Errorf("map size = %d, want 2 (no in-flight evicted)", len(p.entries))
	}
}

// TestPickLRU_SkipsExceptKey: the about-to-be-created key, even if already
// present (recreate path), is never chosen as its own LRU victim.
func TestPickLRU_SkipsExceptKey(t *testing.T) {
	clk := time.Unix(1_000_000, 0)
	p := newTestPool(Config{MaxServers: 1}, &clk)
	seedReady(p, "self", fakeServer(), clk.Add(-time.Hour))

	p.mu.Lock()
	victim := p.pickLRULocked("self")
	p.mu.Unlock()

	if victim != nil {
		t.Error("must not pick the except-key as its own LRU victim")
	}
}

// --- gopls-gated integration tests (-race) ---------------------------------

// TestPool_DefaultBehaviorUnchanged: a zero Config keeps the original semantics
// — no idle eviction, server lives until Close. (Sanity that the lifecycle code
// is inert by default.)
func TestPool_DefaultBehaviorUnchanged(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}
	root, shape, _ := writeFixture(t)
	pool := NewPool(Config{})
	t.Cleanup(func() { _ = pool.Close() })
	warm(t, pool, root, shape)

	// A query for the same root again must not evict (IdleTTL=0).
	at := identPos(t, shape, "type Circle struct", "Circle")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.References(ctx, at, true); err != nil {
		t.Fatalf("References: %v", err)
	}
	if s := pool.Stats(); s.Live != 1 || s.Evictions != 0 {
		t.Errorf("default Config: Live=%d Evictions=%d, want 1/0", s.Live, s.Evictions)
	}
}

// TestPool_IdleTTLEviction creates a server, advances the virtual clock past a
// tiny IdleTTL, then issues a Navigator for a DIFFERENT root — the sweep at the
// top of Navigator must evict and Close the now-idle first server. Virtual clock
// keeps it sub-second and flake-free.
func TestPool_IdleTTLEviction(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}
	rootA, shapeA, _ := writeFixture(t)
	rootB, shapeB, _ := writeFixture(t)

	var clk virtClock
	clk.set(time.Unix(2_000_000, 0))
	pool := NewPool(Config{IdleTTL: time.Minute, now: clk.get})
	t.Cleanup(func() { _ = pool.Close() })

	warm(t, pool, rootA, shapeA)
	navA, err := pool.Navigator(context.Background(), rootA)
	if err != nil {
		t.Fatalf("Navigator A: %v", err)
	}

	// Advance virtual time past the TTL, then touch a different root to trigger
	// the lazy sweep of A.
	clk.set(clk.get().Add(2 * time.Minute))
	warm(t, pool, rootB, shapeB)

	// A must be gone from the map and its process closed.
	deadline := time.Now().Add(10 * time.Second)
	for navA.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if navA.Alive() {
		t.Error("idle server A should have been Closed by the sweep")
	}
	pool.mu.Lock()
	_, hasA := pool.entries[keyFor("go", rootA)]
	pool.mu.Unlock()
	if hasA {
		t.Error("idle server A should have been removed from entries")
	}
}

// TestPool_BackoffCancellation asserts a lane whose ctx is cancelled during the
// restart backoff returns promptly with ctx.Err() instead of sleeping the whole
// delay. Uses a large BaseBackoff so a prompt return is unambiguous; no gopls
// needed for the cancel path, but we gate to keep the suite uniform and use a
// real (dead) entry shape.
func TestPool_BackoffCancellation(t *testing.T) {
	clk := time.Unix(3_000_000, 0)
	// Large backoff: if cancellation didn't short-circuit, the call would block
	// for seconds.
	p := newTestPool(Config{BaseBackoff: 10 * time.Second, MaxBackoff: 10 * time.Second}, &clk)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := p.sleepBackoff(ctx, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx.Err() from cancelled backoff")
	}
	if elapsed > time.Second {
		t.Errorf("cancelled backoff slept %v; expected prompt return", elapsed)
	}
}

// TestPool_MaxServersLRUIntegration: with MaxServers=1, creating a server for a
// second root must evict the first (it is idle and ready). gopls-gated.
func TestPool_MaxServersLRUIntegration(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}
	rootA, shapeA, _ := writeFixture(t)
	rootB, shapeB, _ := writeFixture(t)

	pool := NewPool(Config{MaxServers: 1})
	t.Cleanup(func() { _ = pool.Close() })

	warm(t, pool, rootA, shapeA)
	navA, err := pool.Navigator(context.Background(), rootA)
	if err != nil {
		t.Fatalf("Navigator A: %v", err)
	}
	pool.mu.Lock()
	n := len(pool.entries)
	pool.mu.Unlock()
	if n != 1 {
		t.Fatalf("after A: %d entries, want 1", n)
	}

	// Creating B at cap 1 evicts A (the LRU idle server).
	warm(t, pool, rootB, shapeB)

	deadline := time.Now().Add(10 * time.Second)
	for navA.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if navA.Alive() {
		t.Error("LRU server A should have been Closed when B exceeded MaxServers")
	}
	pool.mu.Lock()
	n = len(pool.entries)
	pool.mu.Unlock()
	if n != 1 {
		t.Errorf("at MaxServers=1, %d live entries, want 1", n)
	}
}

// TestPool_CloseDuringCreation hammers Navigator across distinct + shared roots
// while Close() runs concurrently, then asserts no panic/race and (BUG#1
// regression) no leaked process: every nav returned after Close is itself dead.
func TestPool_CloseDuringCreation(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}
	roots := make([]string, 4)
	for i := range roots {
		roots[i], _, _ = writeFixture(t)
	}

	pool := NewPool(Config{})

	const lanes = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var returned []*LSP

	wg.Add(lanes)
	for i := range lanes {
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nav, err := pool.Navigator(ctx, roots[i%len(roots)])
			if err == nil && nav != nil {
				mu.Lock()
				returned = append(returned, nav)
				mu.Unlock()
			}
		}(i)
	}
	// Close concurrently with the creation storm.
	go func() { _ = pool.Close() }()
	wg.Wait()
	_ = pool.Close() // ensure fully closed

	// Give any backgrounded nav.Close() goroutines a moment to take effect.
	deadline := time.Now().Add(10 * time.Second)
	for {
		allDead := true
		mu.Lock()
		for _, n := range returned {
			if n.Alive() {
				allDead = false
				break
			}
		}
		mu.Unlock()
		if allDead || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, n := range returned {
		if n.Alive() {
			t.Errorf("nav %d still Alive after Close — leaked server (BUG#1)", i)
		}
	}
}
