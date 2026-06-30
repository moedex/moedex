package navigate

// No build tag: these tests pin the F-022 fix — NavigatorFor must persist a
// per-(root,language) cooldown across calls so a key that crashes on every
// startup fails fast on the next call instead of re-spawning up to
// maxRestart times, forever. They exercise the real NavigatorFor creation
// path (no fakes) by pointing Config.Server at a nonexistent absolute path:
// NewLSP fails deterministically and fast on that in BOTH build arms — the
// default arm returns errNoLSP unconditionally, and the lsp arm's cmd.Start()
// fails with ENOENT before any process exists. No gopls required.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// bogusServerPath returns an absolute path that cannot possibly exist, so
// every NewLSP attempt against it fails immediately and deterministically.
func bogusServerPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "moedex-test-nonexistent-lsp-binary")
}

// --- pure unit tests of the cooldown helpers (no NewLSP, no Pool loop) -----

// TestCooldown_RecordThenStatus pins the basic lifecycle: recordExhausted
// arms a cooldown that cooldownStatus reports active until it expires, after
// which the entry is pruned from the map (mirrors sweepIdleLocked's lazy GC).
func TestCooldown_RecordThenStatus(t *testing.T) {
	p := NewPool(Config{BaseBackoff: time.Second, MaxBackoff: 10 * time.Second})
	t.Cleanup(func() { _ = p.Close() })

	now := time.Unix(1_000_000, 0)
	k := keyFor("go", "/some/root")

	delay := p.recordExhausted(k, now)
	if delay != time.Second {
		t.Fatalf("first exhaustion delay = %v, want base %v", delay, time.Second)
	}

	active, until := p.cooldownStatus(k, now)
	if !active {
		t.Fatal("expected key to be in cooldown immediately after recordExhausted")
	}
	if !until.Equal(now.Add(delay)) {
		t.Errorf("until = %v, want %v", until, now.Add(delay))
	}

	// Still within the window a moment later.
	active, _ = p.cooldownStatus(k, now.Add(delay/2))
	if !active {
		t.Error("expected cooldown still active before it expires")
	}

	// Past the window: inactive, and pruned from the map.
	active, _ = p.cooldownStatus(k, now.Add(delay+time.Millisecond))
	if active {
		t.Error("expected cooldown to have expired")
	}
	p.mu.Lock()
	_, stillThere := p.cooldowns[k]
	p.mu.Unlock()
	if stillThere {
		t.Error("expired cooldown entry should have been pruned from the map")
	}
}

// TestCooldown_StreakGrowsAcrossCalls is the heart of F-022: repeated
// exhaustion of the SAME key must grow the cooldown like the in-call backoff
// grows with repeated attempts — not reset to the base delay every time,
// which is exactly what happened when the streak lived in NavigatorFor's
// loop-local failCount instead of on the Pool.
func TestCooldown_StreakGrowsAcrossCalls(t *testing.T) {
	base, max := 100*time.Millisecond, 10*time.Second
	p := NewPool(Config{BaseBackoff: base, MaxBackoff: max})
	t.Cleanup(func() { _ = p.Close() })

	now := time.Unix(2_000_000, 0)
	k := keyFor("go", "/another/root")

	want := []time.Duration{base, 2 * base, 4 * base, 8 * base}
	for i, w := range want {
		got := p.recordExhausted(k, now)
		if got != w {
			t.Errorf("exhaustion #%d delay = %v, want %v", i+1, got, w)
		}
		now = now.Add(got) // simulate the next call arriving right as cooldown expires
	}
}

// TestCooldown_ClearResetsStreak asserts a successful creation (simulated
// directly, the way NavigatorFor's spawn-success path does) resets the streak
// so the NEXT failure starts the backoff fresh at the base delay rather than
// carrying forward an inflated streak from before the server recovered.
func TestCooldown_ClearResetsStreak(t *testing.T) {
	base, max := 50*time.Millisecond, 5*time.Second
	p := NewPool(Config{BaseBackoff: base, MaxBackoff: max})
	t.Cleanup(func() { _ = p.Close() })

	now := time.Unix(3_000_000, 0)
	k := keyFor("go", "/yet/another/root")

	if got := p.recordExhausted(k, now); got != base {
		t.Fatalf("first exhaustion delay = %v, want base %v", got, base)
	}
	if got := p.recordExhausted(k, now); got != 2*base {
		t.Fatalf("second exhaustion delay = %v, want %v", got, 2*base)
	}

	// Simulate NavigatorFor's spawn-success path: clear the cooldown.
	p.mu.Lock()
	delete(p.cooldowns, k)
	p.mu.Unlock()

	if got := p.recordExhausted(k, now); got != base {
		t.Fatalf("post-recovery exhaustion delay = %v, want base %v (streak should have reset)", got, base)
	}
}

// --- integration tests through the real NavigatorFor path ------------------

// TestNavigatorFor_CooldownStopsRespawnStorm is the F-022 regression test: a
// key whose server crashes on every startup must NOT be re-spawned up to
// maxRestart times on every single incoming call. The first call exhausts
// maxRestart attempts (as designed); a second call arriving immediately
// after must fail fast from the cooldown, making zero additional spawn
// attempts, instead of repeating the same maxRestart spin.
func TestNavigatorFor_CooldownStopsRespawnStorm(t *testing.T) {
	cfg := Config{
		Server:      bogusServerPath(t),
		BaseBackoff: time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
	}
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.Close() })

	ctx := context.Background()
	root := t.TempDir()

	if _, err := pool.NavigatorFor(ctx, "go", root); err == nil {
		t.Fatal("expected an error: the configured server can never start")
	} else if errors.Is(err, ErrCooldown) {
		t.Fatalf("first call should fail from exhausted retries, not a pre-existing cooldown: %v", err)
	}

	afterFirst := pool.Stats().SpawnAttempts
	if afterFirst != int64(maxRestart) {
		t.Fatalf("after first call: SpawnAttempts = %d, want exactly %d", afterFirst, maxRestart)
	}

	// Second call, same (root,language), arriving immediately: must fail fast
	// from the cooldown and must NOT attempt to spawn again.
	_, err := pool.NavigatorFor(ctx, "go", root)
	if err == nil {
		t.Fatal("expected an error: the key should still be cooling down")
	}
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("second call error = %v, want it to wrap ErrCooldown", err)
	}

	stats := pool.Stats()
	if stats.SpawnAttempts != afterFirst {
		t.Fatalf("F-022 regression: second call made %d new spawn attempts while cooling down (SpawnAttempts %d -> %d); a perpetually-crashing key must not be re-spawned on every call",
			stats.SpawnAttempts-afterFirst, afterFirst, stats.SpawnAttempts)
	}
	if stats.CooldownRejects != 1 {
		t.Errorf("CooldownRejects = %d, want 1", stats.CooldownRejects)
	}
}

// TestNavigatorFor_CooldownIsPerKey asserts the cooldown is scoped to its own
// (root,language) key: a different root that has never failed must spawn
// normally (and fail on its own merits) even while an unrelated key is
// cooling down.
func TestNavigatorFor_CooldownIsPerKey(t *testing.T) {
	cfg := Config{
		Server:      bogusServerPath(t),
		BaseBackoff: time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
	}
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.Close() })

	ctx := context.Background()
	rootA, rootB := t.TempDir(), t.TempDir()

	if _, err := pool.NavigatorFor(ctx, "go", rootA); err == nil {
		t.Fatal("expected an error for rootA")
	}
	afterA := pool.Stats().SpawnAttempts

	if _, err := pool.NavigatorFor(ctx, "go", rootB); err == nil {
		t.Fatal("expected an error for rootB")
	} else if errors.Is(err, ErrCooldown) {
		t.Fatalf("rootB must not be rejected by rootA's cooldown: %v", err)
	}

	afterB := pool.Stats().SpawnAttempts
	if afterB != afterA+int64(maxRestart) {
		t.Fatalf("rootB SpawnAttempts delta = %d, want %d (it must spawn its own full attempt budget, unaffected by rootA's cooldown)", afterB-afterA, maxRestart)
	}
}

// TestNavigatorFor_CooldownExpiresThenRetries advances a virtual clock past
// the cooldown window and asserts the next call attempts to spawn again
// rather than being rejected forever — the cooldown is a backoff, not a
// permanent ban.
func TestNavigatorFor_CooldownExpiresThenRetries(t *testing.T) {
	clk := time.Unix(1_700_000_000, 0)
	cfg := Config{
		Server:      bogusServerPath(t),
		BaseBackoff: time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
		now:         func() time.Time { return clk },
	}
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.Close() })

	ctx := context.Background()
	root := t.TempDir()

	if _, err := pool.NavigatorFor(ctx, "go", root); err == nil {
		t.Fatal("expected an error")
	}
	attempts := pool.Stats().SpawnAttempts

	if _, err := pool.NavigatorFor(ctx, "go", root); !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown immediately after exhaustion, got %v", err)
	}
	if got := pool.Stats().SpawnAttempts; got != attempts {
		t.Fatalf("spawn attempts changed while still cooling down: %d -> %d", attempts, got)
	}

	// Advance the virtual clock well past the cooldown window.
	clk = clk.Add(time.Hour)

	if _, err := pool.NavigatorFor(ctx, "go", root); err == nil {
		t.Fatal("expected an error: the server still can't start")
	} else if errors.Is(err, ErrCooldown) {
		t.Fatal("expected a fresh exhaustion error, not ErrCooldown, once the cooldown window has passed")
	}
	if got := pool.Stats().SpawnAttempts; got <= attempts {
		t.Fatalf("expected new spawn attempts once the cooldown expired, got %d (was %d)", got, attempts)
	}
}
