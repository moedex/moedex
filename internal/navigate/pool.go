package navigate

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moedex/internal/ingest"
)

// Pool is the ADR 0017 Condition-2 answer: concurrency-safe navigation under
// many simultaneous parallel lanes.
//
// The naive reading of Condition 2 — "language servers are heavy, so spawning
// one per lane regresses startup cost" — points the wrong way. A language server
// already multiplexes concurrent requests internally, so the safe design is the
// same one Serena was chosen for: **one server per project root, shared across
// all lanes**, not one per lane. Pool keys a single server per module root,
// creates it lazily without a thundering herd (concurrent first-callers for the
// same root wait on one creation, not N), routes each query to the right root,
// and transparently restarts a server that has died (servers are "flaky under
// churn"). A dead/failed entry is evicted so the next call gets a fresh one.
//
// Pool is safe for concurrent use by any number of goroutines. The per-server
// LSP client is likewise hardened (atomic ids, mutex-guarded pending map,
// serialized writes, exactly-once didOpen), so sharing one server across lanes
// does not corrupt state — the property `codebase-memory-mcp` failed on.
//
// Note Pool compiles in both build arms; in the default (no -tags lsp) build the
// underlying NewLSP returns the "rebuild with -tags lsp" error, which Pool
// surfaces unchanged.
type Pool struct {
	cfg        Config
	workspaces *workspaceManager

	mu      sync.Mutex
	entries map[string]*poolEntry // key: keyFor(lang, root) — one server per (root,language)
	closed  bool

	// Lifetime activity counters, read atomically by Stats. They live on Pool
	// (which compiles in both build arms) and are purely numeric, so they add no
	// dependency on any -tags lsp symbol and keep the default pure-Go build green.
	nQueries         atomic.Int64
	nSpawns          atomic.Int64
	nSpawnAttempts   atomic.Int64
	nRestarts        atomic.Int64
	nEvictions       atomic.Int64
	nCooldownRejects atomic.Int64

	// cooldowns tracks, per (root,language) key, a consecutive-exhaustion streak
	// that survives across NavigatorFor calls (unlike the loop-local failCount
	// below). See recordExhausted for why this is the F-022 fix.
	cooldowns map[string]*poolCooldown

	// closeWG tracks the detached server-close goroutines spawned by eviction,
	// idle-sweep, LRU, and restart paths, so Close() can join them and is a true
	// teardown barrier (no language-server child outliving Close()'s return).
	closeWG sync.WaitGroup
}

// poolCooldown is a key's "do not respawn before until" gate plus the streak
// of consecutive NavigatorFor calls that have exhausted all maxRestart
// attempts since the key last produced a live server. Guarded by Pool.mu.
type poolCooldown struct {
	until  time.Time
	streak int
}

// ErrCooldown is wrapped by the error NavigatorFor returns when it fast-fails
// a call for a key that is still cooling down from a prior call's exhausted
// retries, so callers can branch on errors.Is(err, ErrCooldown). Lives in
// pool.go (no build tag) so it is identical in both build arms.
var ErrCooldown = errors.New("navigate: server cooling down after repeated failures")

// ErrPrivacyRestricted is wrapped by the error NavigatorFor returns when a
// workspace's .ai-privacy.yml marks it Restricted (level 1). F-03: an external
// language server can scan the entire workspace after a single query, not just
// the file that was asked about, so this check has to gate the spawn itself
// structurally — inside Pool, the one place every current caller (moedex-serve's
// LSP tools, `moe nav`, internal/graph/build's graph pass) and any future one
// shares — rather than relying on each caller to remember to preflight it the
// way internal/graph/build/graphcalls_lsp.go already does. Lives in pool.go (no
// build tag) so it is identical in both build arms.
var ErrPrivacyRestricted = errors.New("navigate: workspace is privacy-restricted (.ai-privacy.yml level 1); refusing to launch an external language server there")

// goClose shuts a server down in the background while letting Close() wait for
// it. Use this instead of a bare `go nav.Close()` for any server removed from
// the pool outside Close itself.
func (p *Pool) goClose(nav *LSP) {
	if nav == nil {
		return
	}
	p.closeWG.Go(func() { _ = nav.Close() })
}

// Stats is an atomically-sampled snapshot of a Pool's lifetime activity. It is
// the ADR 0017 Condition-2 observability surface: spawn / restart / eviction
// counters make the shared-server lifecycle (one server per root, restarted on
// death) visible to callers and to `moe nav -stats`. Zero new deps;
// counters are sync/atomic.Int64 read via Load. Stats lives in pool.go (no build
// tag) so it exists in both build arms.
type Stats struct {
	Queries         int64 `json:"queries"`          // file-routed Definition/References/Implementations calls dispatched
	Spawns          int64 `json:"spawns"`           // language servers successfully created (NewLSP ok)
	SpawnAttempts   int64 `json:"spawn_attempts"`   // every NewLSP call attempted, success or failure
	Restarts        int64 `json:"restarts"`         // dead/errored servers evicted-and-recreated by Navigator's retry loop
	Evictions       int64 `json:"evictions"`        // entries removed from the pool (evict() calls)
	CooldownRejects int64 `json:"cooldown_rejects"` // NavigatorFor calls fast-failed by an active per-key cooldown
	Live            int   `json:"live"`             // servers currently in the entries map at snapshot time
}

// Stats returns an atomically-sampled snapshot of the Pool's lifetime activity.
// It is safe to call concurrently with any other Pool method. Live reuses p.mu
// briefly (the same lock guarding entries), consistent with existing usage.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	live := len(p.entries)
	p.mu.Unlock()
	return Stats{
		Queries:         p.nQueries.Load(),
		Spawns:          p.nSpawns.Load(),
		SpawnAttempts:   p.nSpawnAttempts.Load(),
		Restarts:        p.nRestarts.Load(),
		Evictions:       p.nEvictions.Load(),
		CooldownRejects: p.nCooldownRejects.Load(),
		Live:            live,
	}
}

// poolEntry is one lazily-created server. ready is closed once creation
// finishes; concurrent callers block on it instead of racing to spawn.
type poolEntry struct {
	ready chan struct{}
	nav   *LSP
	err   error

	// lastUsed is the unix-nanos timestamp of the most recent successful handout.
	// It is atomic so the hot cache-hit path can record usage (touch) without
	// taking pool.mu — preserving the low-contention profile the 32-lane and
	// thundering-herd concurrency tests depend on. Read under pool.mu by the
	// idle-sweep and LRU paths.
	lastUsed atomic.Int64
}

// touch records that ent was just handed out at now. Lock-free by design.
func (ent *poolEntry) touch(now time.Time) { ent.lastUsed.Store(now.UnixNano()) }

// maxRestart bounds how many times a single Navigator call will evict a
// dead-on-arrival server and retry, so a server that crashes immediately on
// startup fails fast instead of spinning forever.
const maxRestart = 3

// Default backoff bounds used when Config leaves them zero.
const (
	defaultBaseBackoff = 200 * time.Millisecond
	defaultMaxBackoff  = 5 * time.Second
)

// backoff returns the restart-retry delay for the given (zero-based) attempt:
// min(max, base * 2^attempt), clamped non-negative. Pure and unit-testable with
// no server. The caller applies full jitter (rand.N(delay)) on top.
func backoff(attempt int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = defaultBaseBackoff
	}
	if max <= 0 {
		max = defaultMaxBackoff
	}
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if d < 0 { // overflow guard
		d = max
	}
	return d
}

// cooldownStatus reports whether key k is still cooling down at now from a
// prior call's exhausted retries, and if so when that cooldown expires. A
// key in cooldown is fast-failed by NavigatorFor before it ever touches the
// entries map or calls NewLSP — the F-022 fix: without this, a server that
// crashes on every startup got re-spawned up to maxRestart times per call,
// forever, because the failure streak lived only in NavigatorFor's loop-local
// failCount and was discarded the instant that call returned. Expired entries
// are pruned lazily here, same approach as sweepIdleLocked, so a key that has
// since recovered never lingers in the map. Must be called with p.mu free
// (it takes the lock itself).
func (p *Pool) cooldownStatus(k string, now time.Time) (active bool, until time.Time) {
	p.mu.Lock()
	cd, ok := p.cooldowns[k]
	if !ok {
		p.mu.Unlock()
		return false, time.Time{}
	}
	if now.Before(cd.until) {
		until = cd.until
		p.mu.Unlock()
		return true, until
	}
	delete(p.cooldowns, k)
	p.mu.Unlock()
	return false, time.Time{}
}

// recordExhausted bumps key k's consecutive-exhaustion streak and (re)arms its
// cooldown so the next NavigatorFor call for this known-bad key fails fast
// instead of re-spawning. Only called after a call has burned through every
// maxRestart attempt without producing a live server — never on caller
// cancellation, which is a property of the caller, not the server. The streak
// lives on the Pool rather than the call stack, so repeated calls for a
// perpetually-crashing key back off further each time, mirroring how the
// in-call backoff grows with repeated attempts within a single call. A
// successful creation (see NavigatorFor's spawn-success path) clears the
// streak, so a server that recovers starts the backoff fresh on its next
// failure rather than carrying forward an inflated streak.
func (p *Pool) recordExhausted(k string, now time.Time) time.Duration {
	p.mu.Lock()
	cd := p.cooldowns[k]
	if cd == nil {
		cd = &poolCooldown{}
		p.cooldowns[k] = cd
	}
	cd.streak++
	delay := backoff(cd.streak-1, p.cfg.BaseBackoff, p.cfg.MaxBackoff)
	cd.until = now.Add(delay)
	p.mu.Unlock()
	return delay
}

// NewPool returns a Pool that creates servers from cfg. cfg.RootDir is ignored
// for routing — each query's (language, root) is derived from its file — but
// cfg.Server / cfg.Args carry through to every spawned server. When cfg.Server
// is set the Pool is in legacy single-server mode: it spawns that one command
// for every (root,language) and ignores per-language registry specs (this keeps
// `moe nav`'s -server override intact). When cfg.Server is empty each
// (root,language) launches its registry server.
func NewPool(cfg Config) *Pool {
	// Fill zero values with defaults so a zero Config behaves exactly like the
	// original Pool: no idle TTL, no server cap. Only the backoff bounds get
	// non-zero defaults, and they fire only on the death-retry path (which used
	// to spin immediately) — so steady-state behavior is unchanged.
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = defaultBaseBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	return &Pool{
		cfg:        cfg,
		workspaces: newWorkspaceManager(cfg.WorkspaceCacheDir),
		entries:    make(map[string]*poolEntry),
		cooldowns:  make(map[string]*poolCooldown),
	}
}

// clock returns the current time through the test seam (cfg.now) when set,
// otherwise time.Now. Every lifecycle timestamp (touch, idle sweep, LRU pick)
// flows through here so virtual-time tests are deterministic and -race clean.
func (p *Pool) clock() time.Time {
	if p.cfg.now != nil {
		return p.cfg.now()
	}
	return time.Now()
}

// keyFor composes a (language, root) pool key. A polyglot monorepo root holds
// one distinct server per language (e.g. ("go",root) and ("typescript",root) are
// separate entries), each routed to by a file's extension. The NUL separator
// keeps the two fields unambiguous. Each (root,language) pair is a separate
// heavyweight process; a deeply polyglot repo can therefore spawn several
// servers — acceptable (Serena spawns one per project too), worth a Pool-size
// note when productionizing.
func keyFor(lang, root string) string { return lang + "\x00" + root }

// Navigator returns the server for a workspace root, creating it on first use.
// PINNED SIGNATURE (ADR 0017 Condition-2 tests call it): it is a back-compat
// shim that treats root as a Go workspace — the language the original spike
// supported — and delegates to NavigatorFor. Existing gopls tests pass go.mod
// roots and route through here unchanged.
func (p *Pool) Navigator(ctx context.Context, root string) (*LSP, error) {
	return p.NavigatorFor(ctx, "go", root)
}

// NavigatorFor returns the server for a (language, workspace-root) pair,
// creating it on first use. Concurrent callers for the same pair share one
// creation and one server. A server discovered to be dead is evicted and
// recreated. In legacy single-server mode (p.cfg.Server set) lang selects the
// pool key but not the command — the configured server handles every language.
//
// Before ever spawning a server for root, this fails closed (ErrPrivacyRestricted,
// wrapped) if root's .ai-privacy.yml marks it Restricted (level 1) — F-03. This
// is the structural gate: callers no longer need their own preflight (see
// ErrPrivacyRestricted's doc for why it lives here instead of at each caller).
func (p *Pool) NavigatorFor(ctx context.Context, lang, root string) (*LSP, error) {
	k := keyFor(lang, root)

	// Fail fast on a key still cooling down from a prior call's exhausted
	// retries, before touching the entries map or calling NewLSP at all (F-022:
	// the cooldown is what survives across calls; failCount below does not).
	checkNow := p.clock()
	if active, until := p.cooldownStatus(k, checkNow); active {
		p.nCooldownRejects.Add(1)
		return nil, fmt.Errorf("navigate: server for (%s,%s) is cooling down after repeated failures, retry after %s: %w", lang, root, until.Sub(checkNow), ErrCooldown)
	}

	failCount := 0 // consecutive create/restart failures for this root, carried
	// across loop iterations (the entry is recreated each spin, so the count
	// can't live on the entry).
	for range maxRestart {
		now := p.clock()

		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("navigate: pool closed")
		}
		// Lazy idle sweep — tie cleanup to traffic. Never sweep the root we are
		// about to (re)use, so we don't evict-then-immediately-recreate.
		victims := p.sweepIdleLocked(now, k)
		ent, ok := p.entries[k]
		if ok {
			p.mu.Unlock()
			p.closeAll(victims) // close evicted servers AFTER releasing pool.mu
			// Wait (or bail on ctx) for the in-flight or finished creation.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ent.ready:
			}
			if ent.err == nil && ent.nav.Alive() {
				ent.touch(now)
				return ent.nav, nil
			}
			// Dead or errored: evict this exact entry and retry fresh after a
			// jittered backoff (a cancelled lane returns immediately).
			p.nRestarts.Add(1)
			p.evict(k, ent)
			failCount++
			if err := p.sleepBackoff(ctx, failCount); err != nil {
				return nil, err
			}
			continue
		}
		// Claim creation for this (root,language). Enforce MaxServers by evicting
		// the LRU idle server while still holding the lock (close after unlock).
		if lru := p.pickLRULocked(k); lru != nil {
			victims = append(victims, lru)
		}
		ent = &poolEntry{ready: make(chan struct{})}
		ent.touch(now) // reserve a sensible lastUsed so an in-flight creation is
		// never chosen as an LRU victim purely for a zero timestamp.
		p.entries[k] = ent
		p.mu.Unlock()
		p.closeAll(victims)

		// F-03 fail-closed privacy preflight, run once per (root,language) key
		// right here — "before any process is spawned" — rather than on every
		// call: a cache hit above never reaches this line. Unlike a NewLSP
		// failure this returns immediately instead of feeding failCount/
		// sleepBackoff/continue: the same root fails the exact same way every
		// time (it's a local file check, not a flaky process), so looping
		// maxRestart times would just burn backoff delay for nothing AND —
		// worse — let a privacy rejection reach recordExhausted's generic
		// "kept dying after N restarts" error, silently losing the
		// ErrPrivacyRestricted sentinel callers branch on.
		if allowed, perr := ingest.LSPWorkspaceAllowed(root); perr != nil || !allowed {
			if perr == nil {
				perr = ErrPrivacyRestricted
			}
			err := fmt.Errorf("navigate: privacy preflight %s: %w", root, perr)
			ent.err = err
			close(ent.ready)
			p.evict(k, ent) // don't cache a failed creation
			return nil, err
		}

		serverRoot, pathMap, isolateErr := p.workspaces.isolate(ctx, root)
		if isolateErr != nil {
			ent.err = isolateErr
			close(ent.ready)
			p.evict(k, ent)
			return nil, isolateErr
		}
		if pathMap.enabled() {
			if allowed, perr := ingest.LSPWorkspaceAllowed(pathMap.canonicalRepo); perr != nil || !allowed {
				if perr == nil {
					perr = ErrPrivacyRestricted
				}
				err := fmt.Errorf("navigate: privacy preflight %s: %w", pathMap.canonicalRepo, perr)
				ent.err = err
				close(ent.ready)
				p.evict(k, ent)
				return nil, err
			}
		}

		cfg := p.cfg
		cfg.RootDir = serverRoot
		cfg.pathMap = pathMap
		// Registry mode: select the per-language server. Legacy mode (cfg.Server
		// set) keeps the explicit command and ignores lang for selection.
		if cfg.Server == "" {
			cfg.Language = lang
		}
		p.nSpawnAttempts.Add(1)
		nav, err := NewLSP(ctx, cfg)
		ent.nav, ent.err = nav, err
		close(ent.ready)
		if err != nil {
			p.evict(k, ent) // don't cache a failed creation
			failCount++
			if berr := p.sleepBackoff(ctx, failCount); berr != nil {
				return nil, berr
			}
			continue
		}
		// BUG#1 fix: re-check closed after NewLSP. If Close() ran during creation
		// it swapped the map out and never saw this server — return it live and we
		// leak the process. Re-acquire mu, and if the pool closed, undo and close.
		p.mu.Lock()
		if p.closed {
			delete(p.entries, k) // entries was swapped, but be defensive
			p.mu.Unlock()
			p.goClose(nav)
			return nil, fmt.Errorf("navigate: pool closed")
		}
		ent.touch(p.clock())
		delete(p.cooldowns, k) // creation succeeded: clear any stale streak/cooldown for k
		p.mu.Unlock()
		p.nSpawns.Add(1)
		return nav, nil
	}
	delay := p.recordExhausted(k, p.clock())
	return nil, fmt.Errorf("navigate: server for (%s,%s) kept dying after %d restarts; cooling down for %s before the next attempt", lang, root, maxRestart, delay)
}

// sleepBackoff sleeps for a jittered backoff appropriate to attempt (>=1),
// returning ctx.Err() if the lane is cancelled mid-sleep instead of sleeping the
// whole delay. attempt-1 is the zero-based exponent so the first retry uses the
// base delay.
func (p *Pool) sleepBackoff(ctx context.Context, attempt int) error {
	delay := backoff(attempt-1, p.cfg.BaseBackoff, p.cfg.MaxBackoff)
	if delay <= 0 {
		return nil
	}
	// Full jitter: actual in [0, delay). rand.N from math/rand/v2 is lock-free
	// and auto-seeded — no shared Source, no manual seeding, no flake.
	actual := rand.N(delay)
	if actual <= 0 {
		return nil
	}
	t := time.NewTimer(actual)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// sweepIdleLocked removes every entry whose creation has finished, succeeded,
// and whose last use is at least IdleTTL ago — except the key currently being
// requested (except). It must be called with p.mu held; it deletes victims from
// the map under the lock and RETURNS them so the caller can Close them AFTER
// unlocking (LSP.Close blocks on process exit / <-ent.ready and must never run
// under pool.mu). With IdleTTL==0 it is a no-op, preserving original behavior.
//
// Weakness (documented, acceptable): a fully idle Pool never calls Navigator and
// therefore never sweeps. An idle Pool consumes no new servers, and Close()
// reaps everything; a caller wanting hard idle reclamation drives an external
// ticker calling Sweep(). We deliberately do NOT run an internal reaper
// goroutine (leak risk / contradicts ADR 0017 statelessness).
func (p *Pool) sweepIdleLocked(now time.Time, except string) []*LSP {
	ttl := p.cfg.IdleTTL
	if ttl <= 0 {
		return nil
	}
	var victims []*LSP
	for k, ent := range p.entries {
		if k == except {
			continue
		}
		// Never block the sweep on an in-flight creation.
		select {
		case <-ent.ready:
		default:
			continue
		}
		if ent.err != nil || ent.nav == nil {
			continue
		}
		idle := now.Sub(time.Unix(0, ent.lastUsed.Load()))
		if idle < ttl {
			continue
		}
		delete(p.entries, k)
		p.nEvictions.Add(1)
		victims = append(victims, ent.nav)
	}
	return victims
}

// pickLRULocked enforces MaxServers: when the map is at/over the cap and a new
// key (except) is about to be inserted, it removes and returns the
// least-recently-used eligible server (creation finished, no error, not the new
// key) so the caller can Close it after unlocking. Returns nil when no eviction
// is needed or no eligible victim exists (e.g. every other entry is mid-creation
// — a transient soft overshoot rather than killing an in-flight creation or
// failing the lane). Must be called with p.mu held. O(n) over roots (tiny).
func (p *Pool) pickLRULocked(except string) *LSP {
	max := p.cfg.MaxServers
	if max <= 0 || len(p.entries) < max {
		return nil
	}
	var (
		victimKey string
		victim    *poolEntry
		oldest    int64
	)
	for k, ent := range p.entries {
		if k == except {
			continue
		}
		select {
		case <-ent.ready:
		default:
			continue // in-flight creation — never evict it
		}
		if ent.err != nil || ent.nav == nil {
			continue
		}
		lu := ent.lastUsed.Load()
		if victim == nil || lu < oldest {
			victimKey, victim, oldest = k, ent, lu
		}
	}
	if victim == nil {
		// Soft overshoot: all peers are cold creations. Preserve liveness rather
		// than fail the lane; the next sweep/creation reclaims.
		return nil
	}
	delete(p.entries, victimKey)
	p.nEvictions.Add(1)
	return victim.nav
}

// closeAll closes each victim server in the background, mirroring evict's
// fire-and-forget semantics so Navigator latency stays flat. Safe with a nil or
// empty slice.
func (p *Pool) closeAll(victims []*LSP) {
	for _, v := range victims {
		p.goClose(v)
	}
}

// Sweep forces an idle-TTL sweep immediately rather than waiting for the next
// Navigator call. It is the hook a caller (e.g. moedex-serve on reload, or an
// external ticker) uses for hard idle reclamation, since the Pool runs no
// internal reaper goroutine. No-op when IdleTTL is 0 or the pool is closed.
func (p *Pool) Sweep() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	victims := p.sweepIdleLocked(p.clock(), "")
	p.mu.Unlock()
	p.closeAll(victims)
}

// evict removes ent from the map only if it is still the current entry for key
// (so we never drop a healthy replacement another goroutine just installed), and
// closes its server in the background. It is also called on a failed creation
// (ent.nav == nil — nothing was ever spawned), so the eviction counter only
// moves when a real server is being removed; otherwise Spawns-Evictions would
// not be a clean "live servers" signal.
func (p *Pool) evict(key string, ent *poolEntry) {
	p.mu.Lock()
	if p.entries[key] == ent {
		delete(p.entries, key)
		if ent.nav != nil {
			p.nEvictions.Add(1)
		}
	}
	p.mu.Unlock()
	p.goClose(ent.nav)
}

// routingLang resolves the language used for both the pool key and (via
// NavigatorFor's "cfg.Server == "" => cfg.Language = lang") the per-spawn
// Config.Language, given fileLang — the file's OWN extension-derived language
// from workspaceRoot. A Pool configured with a pinned Config.Language (forced
// -lang mode: the nav command's buildConfig sets Config{Language: canon} with
// Server left empty so NewLSP's registry branch resolves InitOptions/
// ResolveEnv) must route EVERY file to that one pinned language, not the
// file's own — otherwise the first query against a file whose extension
// disagrees with the forced language would silently clobber it back to
// auto-detected routing. Legacy override mode (Server set) is unaffected: per
// the documented NavigatorFor contract, lang still selects the pool key but
// never the launched command, so falling through to fileLang here is correct.
func (p *Pool) routingLang(fileLang string) string {
	if p.cfg.Server == "" && p.cfg.Language != "" {
		return p.cfg.Language
	}
	return fileLang
}

// --- file-routed convenience queries ---------------------------------------
//
// These resolve the file's (language, workspace-root), fetch (or create) that
// pair's server, and run the query — the ergonomic entry point for a lane that
// just has a cursor position and doesn't want to manage roots. For a Go file
// workspaceRoot yields ("go", <go.mod dir>), identical to the original
// moduleRoot routing; a polyglot repo routes each file to its language's
// server — unless the Pool was configured with a pinned Config.Language
// (forced -lang mode), in which case routingLang overrides the per-file
// language while workspaceRoot's root resolution is unaffected.

func (p *Pool) Definition(ctx context.Context, at Pos) ([]Location, error) {
	return legacyLocations(p.DefinitionDetailed(ctx, at))
}

// DefinitionDetailed is Definition with an outcome that distinguishes a
// successful empty response, unsupported method, and unavailable server.
func (p *Pool) DefinitionDetailed(ctx context.Context, at Pos) (LocationQueryResult, error) {
	p.nQueries.Add(1)
	fileLang, root := workspaceRoot(at.File)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return LocationQueryResult{Status: LocationQueryUnavailable}, err
	}
	return nav.DefinitionDetailed(ctx, at)
}

func (p *Pool) References(ctx context.Context, at Pos, includeDecl bool) ([]Location, error) {
	return legacyLocations(p.ReferencesDetailed(ctx, at, includeDecl))
}

// ReferencesDetailed is References with a status-preserving result.
func (p *Pool) ReferencesDetailed(ctx context.Context, at Pos, includeDecl bool) (LocationQueryResult, error) {
	p.nQueries.Add(1)
	fileLang, root := workspaceRoot(at.File)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return LocationQueryResult{Status: LocationQueryUnavailable}, err
	}
	return nav.ReferencesDetailed(ctx, at, includeDecl)
}

func (p *Pool) Implementations(ctx context.Context, at Pos) ([]Location, error) {
	return legacyLocations(p.ImplementationsDetailed(ctx, at))
}

// ImplementationsDetailed is Implementations with a status-preserving result.
func (p *Pool) ImplementationsDetailed(ctx context.Context, at Pos) (LocationQueryResult, error) {
	p.nQueries.Add(1)
	fileLang, root := workspaceRoot(at.File)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return LocationQueryResult{Status: LocationQueryUnavailable}, err
	}
	return nav.ImplementationsDetailed(ctx, at)
}

// DocumentSymbol is file-routed exactly like Definition/References/
// Implementations above: it resolves (language, root) from the file's own
// extension and creates that server on demand.
func (p *Pool) DocumentSymbol(ctx context.Context, file string) ([]Symbol, error) {
	p.nQueries.Add(1)
	fileLang, root := workspaceRoot(file)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return nil, err
	}
	return nav.DocumentSymbol(ctx, file)
}

// WorkspaceSymbol is the ADR 0018 root-routed name lookup: workspace/symbol has
// no file to route by, so the caller supplies root directly.
//
// With lang set, it behaves like the file-routed queries above: the (lang,
// root) server is created on demand if it isn't already live.
//
// With lang empty (the polyglot case), it merges every server ALREADY LIVE for
// root across every language — it never spawns one purely to answer a name
// search, so a cold root with no live server yet returns (nil, nil) rather
// than fanning out to every registered language. This is deliberate "coverage
// honesty" (ADR 0018): only languages whose server is up contribute, and a
// caller that wants full multi-language coverage should have already warmed
// the languages it cares about via a file-routed call. A live server that
// errors mid-query is skipped rather than failing the whole merge, so one
// flaky/dying peer never blanks out the others' results.
func (p *Pool) WorkspaceSymbol(ctx context.Context, root, lang, query string) ([]Symbol, error) {
	p.nQueries.Add(1)
	if lang != "" {
		nav, err := p.NavigatorFor(ctx, lang, root)
		if err != nil {
			return nil, err
		}
		return nav.WorkspaceSymbol(ctx, query)
	}
	var out []Symbol
	for _, nav := range p.liveNavigatorsForRoot(root) {
		syms, err := nav.WorkspaceSymbol(ctx, query)
		if err != nil {
			continue
		}
		out = append(out, syms...)
	}
	return out, nil
}

// liveNavigatorsForRoot returns every already-created, live pooled server for
// root across all languages (keyFor's NUL separator makes a plain suffix match
// exact — a lang value can never itself contain the separator). It never
// creates a server and never blocks on an in-flight creation; both are
// deliberately skipped so WorkspaceSymbol's polyglot merge only ever queries
// servers that were already warm.
func (p *Pool) liveNavigatorsForRoot(root string) []*LSP {
	suffix := "\x00" + root
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*LSP
	for k, ent := range p.entries {
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		select {
		case <-ent.ready:
		default:
			continue // in-flight creation — not live yet
		}
		if ent.err != nil || ent.nav == nil || !ent.nav.Alive() {
			continue
		}
		out = append(out, ent.nav)
	}
	return out
}

// SetOverlay routes an in-memory live-buffer override to the server owning the
// file's (language, workspace-root) (ADR 0017 Condition 3). Subsequent queries
// for that file — from any lane sharing the same pooled server — see the overlay
// until DropOverlay.
func (p *Pool) SetOverlay(ctx context.Context, file string, content []byte) error {
	fileLang, root := workspaceRoot(file)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return err
	}
	return nav.SetOverlay(ctx, file, content)
}

// DropOverlay removes a file's overlay on its owning server, reverting to disk.
func (p *Pool) DropOverlay(ctx context.Context, file string) error {
	fileLang, root := workspaceRoot(file)
	nav, err := p.NavigatorFor(ctx, p.routingLang(fileLang), root)
	if err != nil {
		return err
	}
	return nav.DropOverlay(ctx, file)
}

// NotifyChanged informs the pool that working-tree files changed on disk,
// routing each file to the server that owns its (language, workspace-root) (ADR
// 0017 Condition 3). Files spanning multiple (root,language) pairs are grouped
// and dispatched per server. Only servers that already exist are notified — a
// pair with no live server will read fresh content when it is first created.
func (p *Pool) NotifyChanged(ctx context.Context, files ...string) error {
	byKey := make(map[string][]string)
	for _, f := range files {
		fileLang, root := workspaceRoot(f)
		k := keyFor(p.routingLang(fileLang), root)
		byKey[k] = append(byKey[k], f)
	}
	for k, group := range byKey {
		p.mu.Lock()
		ent, ok := p.entries[k]
		p.mu.Unlock()
		if !ok {
			continue // no server yet; nothing cached to invalidate
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ent.ready:
		}
		if ent.err != nil || ent.nav == nil || !ent.nav.Alive() {
			continue
		}
		if err := ent.nav.NotifyChanged(ctx, group...); err != nil {
			return err
		}
	}
	return nil
}

// Close shuts down every pooled server. Idempotent; further calls error.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	entries := p.entries
	p.entries = make(map[string]*poolEntry)
	p.mu.Unlock()

	var firstErr error
	for _, ent := range entries {
		<-ent.ready // ensure creation finished before closing
		if ent.nav != nil {
			if err := ent.nav.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	// Join any servers being closed asynchronously by prior eviction / idle-sweep
	// / LRU / restart paths, so Close() is a true teardown barrier.
	p.closeWG.Wait()
	return firstErr
}
