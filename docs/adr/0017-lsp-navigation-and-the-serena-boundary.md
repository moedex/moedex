# ADR 0017: LSP-precise navigation — the three conditions to subsume Serena

- **Status:** Proposed. All three conditions are met and the spike has been **productionized**
  in-process (`internal/navigate`, build tag `lsp`, `cmd/moedex-nav`), proven under `make test-lsp`
  (race-enabled). What started single-language (Go/gopls) now ships a multi-language server registry
  (the corpus's real stack — C# ~60%, TypeScript, SCSS/CSS, CFML, HTML, SQL, plus go/python and ready-but-unused rust/cpp), a shared per-`(root,language)` server pool with idle-TTL
  eviction + jittered restart backoff + an optional `MaxServers` LRU bound, editor-grade
  *incremental* (range-based) `didChange` sync, and a full CLI. It remains a **single-host,
  in-process** arm behind `-tags lsp`: cross-process / multi-host server sharing is the one piece of
  the original "remaining work" still unaddressed. Go (gopls), C# (csharp-ls), TypeScript, Python,
  SCSS/CSS, and CFML (cflsp) have all been exercised **live** end-to-end — both through `make test-lsp`
  with the servers installed and through the warm daemon's MCP `find_definition` — and the daemon
  serves the navigation tools alongside `search_context`. CI currently has only gopls installed, so
  the non-Go live tests skip there (env-gated); `registry_test.go` pins the launch recipes
  server-free and always runs. This is still a `Proposed` decision: the engineering proof exists, but
  the strategic move (retiring the Serena seam in Protostar) is not taken here.
- **Date:** 2026-06-29
- **Context owner:** moedex

## Context

[Protostar](../../../protostar) — the orchestration layer that consumes moedex through
[Moe](../../../moe) — uses **Serena** (LSP-precise, runs per-project language servers out of
process) for code *navigation*: `find_symbol` / `find_referencing_symbols` /
`find_implementations` / `find_declaration` / `get_symbols_overview` / `search_for_pattern`.
moedex does *retrieval* (ranked spans). The recurring question is whether a future LSP-backed
moedex makes Serena redundant — collapsing retrieval **and** navigation into one code spine that
Moe fuses once. (See Protostar ADR 0002, "the context seam.")

The answer is *yes in principle, but only when three conditions hold simultaneously.* This ADR
records them so "we added symbols" is never mistaken for "we replaced Serena." The first
condition is the one that quietly invalidates most such claims.

It also matters because Serena was selected over the alternative (`codebase-memory-mcp`) on a
concurrency-safety property, not a feature list — so any replacement is judged on that property,
not just on having navigation features.

## Decision

moedex **does not** target Serena's navigation role today. moedex MAY subsume it later — and
Protostar MAY then retire the separate Serena seam — **only** when all three of the following
hold. Until then, moedex stays a retrieval engine and Serena stays Protostar's live-navigation
adapter.

### Condition 1 — Real LSP semantics, not tree-sitter symbols (the decisive one)

moedex's symbol layer ([0008](./0008-polyglot-symbol-sidecar.md)) is a **syntactic** sidecar:
name + kind + byte offsets, per-blob, no cross-file resolution. ADR 0008 states the negative
explicitly — *"No go-to-def / find-references."* That layer powers symbol-**name** search and
block scoping; it cannot answer "who references this exact symbol" or "what implements this
interface," because those require **type-resolved** results across imports, overloads, and
inheritance — `textDocument/references`-grade resolution, not regex/byte scanners (most of the
0008 extractors) nor even `go/parser` (one file's AST, not a resolved program).

Subsuming Serena requires a true resolution layer (real language servers, or SCIP-grade indexing
that resolves references). Adding more tree-sitter grammars or more 0008-style extractors does
**not** satisfy this condition — it deepens symbol-name recall, which is a different axis.

**Met & productionized (2026-06-29).** `internal/navigate` (build tag `lsp`) drives a real language
server out of process over an LSP JSON-RPC client written against the standard library — **no new
`go.mod` dependency**, mirroring the dense arm's build-tag boundary ([0007](./0007-optional-dense-arm.md)).
It answers `Definition` / `References` / `Implementations`, negotiating the `utf-8`
`positionEncoding` so server columns are byte columns aligned with the byte-offset core
([0002](./0002-positional-trigram-core-byte-offsets.md)). `internal/navigate/lsp_test.go` proves
type-resolved cross-file definition, cross-file references, and interface→implementation
resolution against a self-contained throwaway module — the exact queries 0008 cannot answer.

The single-server (gopls) spike has since generalized to **N languages** via a pure-data registry
(`registry.go`, no build tag): `LangSpec` recipes plus an extension→language map. Each file is routed
to its language's server by extension. The language servers stay **external binaries on PATH, never
linked or imported** — the registry only knows how to launch them, and a server absent on a given
machine skips gracefully (LookPath).

The registry provides explicit capabilities for supported languages.
Shipped entries and their navigation capability:

| Language | Server | def | refs/impl | Notes |
|---|---|---|---|---|
| go | gopls | ✓ | ✓ | the original spike |
| csharp | csharp-ls | ✓ | ✓ | compiler-backed navigation |
| typescript (+js) | typescript-language-server `--stdio` | ✓ | ✓ | also serves `.js`/`.jsx` |
| css/scss/less | vscode-css-language-server `--stdio` | ✓ | ✓ | strongest of the "extra" servers |
| cfml | cflsp (built from softwareCobbler/cfc) | ✓ | — | definition only; needs `initializationOptions.config` |
| html | vscode-html-language-server `--stdio` | ~ | — | shallow; no Angular template↔component (needs Angular LS) |
| sql | sql-language-server `up --method stdio` | — | — | completion-only; no SQL LSP implements navigation |
| python | pyright-langserver `--stdio` | ✓ | ✓ | ~95 files; marginal |
| rust, cpp | rust-analyzer, clangd | ✓ | ✓ | **zero corpus files** — retained as cheap, ready data |

Two facts the implementation got right that are easy to get wrong: rust-analyzer takes **no**
`--stdio` arg (pinned by `registry_test.go`), and **C# needs `DOTNET_ROOT`** when .NET is installed
off the default path (Homebrew) — resolved per-launch by `LangSpec.ResolveEnv` (deriving it from
`dotnet` on PATH, no hardcoded path). C# project roots use a *globbed* `*.csproj`/`*.sln` marker.
Partial-capability servers degrade cleanly: a `textDocument/*` the server doesn't implement returns
JSON-RPC `-32601`, which the client maps to **empty results, not an error** — so `def` works where a
server supports it and `refs`/`impl` come back empty for cflsp/sql rather than throwing. **Condition 1
met** for the resolved-semantics requirement, across the corpus's real languages rather than one.

*Honest gap:* gopls does not echo `positionEncoding` in its initialize reply (LSP nominally defaults
an absent value to utf-16); the client normalizes an absent value to `utf-8` because the entire
byte-column path already depends on it — documented inline, and the safest branch (an undecodable
result) still defaults to full-text sync.

### Condition 2 — Concurrency-safe under parallel lanes

Serena was chosen for Protostar (2026-06-17) **specifically because** per-project language servers
with no shared store are robust under its parallel-lane workflow, while the alternative
(`codebase-memory-mcp`) SIGABRT'd / heap-corrupted under concurrent multi-instance access. An
LSP-backed moedex inherits language-server lifecycle weight: stateful, per-language, heavy startup,
flaky under churn. moedex's current virtue is the opposite — pure-Go ([0001](./0001-single-node-scope-pure-go-default.md)),
stateless retrieval over a warm mmap'd spine ([0010](./0010-warm-serving-spine.md)). An LSP layer
must preserve safe concurrent access under many simultaneous lanes, or it regresses on the exact
property Serena was picked for. This is a hard gate, not a tuning concern.

**Met & productionized (2026-06-29).** `navigate.Pool` reframes this condition. The naive worry —
"servers are heavy, one-per-lane is too costly" — points the wrong way: a language server already
multiplexes concurrent requests, so the safe design is the one Serena itself uses — **one server per
project root, shared across all lanes**, not one per lane. The Pool keys a single server per
`(root, language)` pair, creates it lazily with no thundering-herd double-spawn (concurrent
first-callers for a key wait on one creation), routes each query to the right pair by the file's
extension and workspace root, and transparently restarts a server that has died (servers are "flaky
under churn"). The shared per-server client was hardened to match: atomic request ids, a
mutex-guarded pending map, serialized writes, exactly-once `didOpen`, and — the bug that actually
matters here — the server **process lifetime is decoupled from any request's context**, so one
lane's timeout/cancel cannot kill a server its siblings are still using. A canceled lane also sends a
best-effort `$/cancelRequest` (the process is left untouched), and a request that fails because the
server died returns the typed sentinel `ErrServerDead` (so callers can `errors.Is` on mid-request
death rather than parsing strings).

Beyond the spike, the Pool grew the full server **lifecycle**: lazy idle-TTL eviction (traffic-
triggered plus an external `Sweep()` hook — no internal reaper goroutine, preserving statelessness),
exponential restart backoff with full jitter that honors context cancellation, and an optional
`MaxServers` LRU bound. A close-during-creation process-leak bug was found and fixed (a server
spawned while `Close()` ran is now self-closed). `Stats()` exposes lifetime
spawn/restart/eviction/query counters for observability.
`internal/navigate/pool_test.go` exercises 32 concurrent lanes across two roots, a 24-caller
thundering herd (asserting exactly one server instance), and kill-then-restart, all green under
`go test -race` (the `make test-lsp` gate); the lifecycle paths get dedicated pure (virtual-clock)
unit tests plus gopls-gated integration tests. This satisfies **Condition 2** for the in-process
single-host case.

*Honest gaps:* (1) it does **not** yet address cross-process or multi-host sharing, which moedex's
warm-serving spine ([0010](./0010-warm-serving-spine.md)) would frame separately — this is the one
remaining piece of the original productionizing list. (2) A `(root,language)` key is a distinct
process, so a deeply polyglot monorepo can spawn several servers (Serena spawns one per project too).
(3) A burst of more than `MaxServers` brand-new roots arriving at once is a transient *soft
overshoot* rather than killing an in-flight creation — chosen to preserve lane liveness; the next
sweep/creation reclaims.

### Condition 3 — Live working-tree freshness during Execute

moedex addresses content by **git blob SHA** ([0004](./0004-content-addressable-blob-store.md)) —
a *committed-corpus* view, refreshed at shard granularity ([0011](./0011-shard-level-freshness.md)).
Serena's language servers read the **live buffer**, including uncommitted edits. Navigation during
Protostar's Execute and Verify phases operates on *in-flight* code; a SHA-addressed index is stale
there by construction. Subsuming Serena requires a live / working-tree navigation mode (read the
dirty tree, not just committed blobs) — a genuine functional addition, not a deployment toggle.

**Met & productionized (2026-06-29).** The navigation path is live, not snapshot. Each query
re-reads the working tree and, if the file drifted since the last sync, pushes a versioned
`didChange` so the server's buffer tracks Execute-phase edits. `SetOverlay` goes further: it drives
navigation over an in-memory buffer **that was never written to disk** — the unsaved-buffer case
Serena serves and a SHA-addressed index ([0004](./0004-content-addressable-blob-store.md)) cannot.
`NotifyChanged` is the dependency-free invalidation signal for files edited on disk outside an open
buffer (an orchestrator knows what it edited; this avoids pulling an fsnotify-style watcher in
against [0001](./0001-single-node-scope-pure-go-default.md)). `internal/navigate/live_test.go`
proves a disk edit between two queries changes the reference set, and that an overlay adds a
reference while the on-disk file stays byte-for-byte unchanged, reverting on `DropOverlay`.

The full-text fallback the spike used has been upgraded to **editor-grade incremental
(range-based) `didChange`**: `ensureInit` parses the negotiated `textDocumentSync` capability and
`positionEncoding`, and when the server advertised incremental sync the client emits a single
minimal range edit (a pure `diffRange`/`offsetToPos` computation, byte-column safe under utf-8),
falling back to full-text otherwise; the per-doc baseline only advances after a successful notify.
The diff helpers are pinned by deterministic, always-run unit tests (including a reconstruction
property check and a multibyte-UTF-8 byte-column case), and the live-edit/overlay tests now exercise
the incremental path. **Condition 3 met** for the working-tree/overlay model, now with incremental
sync rather than only full-text.

## Consequences

**Positive (if all three are met)**
- One code spine does ranked retrieval **and** precise navigation; Moe fuses a single, richer
  source instead of joining moedex spans against a separate Serena nav source. Protostar drops an
  external dependency and a seam.
- Navigation inherits moedex's corpus scale, dedup ([0004](./0004-content-addressable-blob-store.md)),
  and warm-serving latency ([0010](./0010-warm-serving-spine.md)).

**Negative / costs**
- Pulls real language-server or SCIP-resolution machinery into moedex's footprint — in tension
  with the pure-Go, zero-dep default ([0001](./0001-single-node-scope-pure-go-default.md)); likely
  belongs behind a build tag / optional sidecar like the dense arm ([0007](./0007-optional-dense-arm.md))
  rather than in the default binary.
- Conditions 2 and 3 were real engineering, not flags — and the arm paid that cost: a
  concurrency-safe shared-server lifecycle (Pool, decoupled process context, per-doc sync locking,
  idle-TTL + LRU eviction, jittered restart backoff) and a live-tree mode (incremental `didChange`
  re-sync, overlays, `NotifyChanged`). The in-process arm is now productionized — multi-language,
  pooled, incrementally-synced, with a `cmd/moedex-nav` CLI (`-verb`, `-lang`/`-server`, `-overlay`,
  `-notify`, `-json`, `-stats`). What remains before retiring the Serena seam is **strategic, not a
  proof gap**: cross-process / multi-host server sharing, CI breadth (the non-Go servers run live
  locally — C#, TypeScript, Python, SCSS/CSS, CFML have all been driven end-to-end — but CI installs
  only gopls, so their live tests skip there), and the Protostar-side decision to actually route
  navigation through Moe (the tripwire below).

**Architectural tripwire (loops back to Protostar ADR 0002)**
- The "layered" context seam keeps navigation on a **direct** Serena seam, deliberately *not*
  routed through Moe. If moedex gains LSP nav **and** Moe already wraps moedex, navigation could
  route through Moe too — which re-opens the rejected "Moe as single front door" option. An
  LSP-backed moedex meeting these three conditions is the specific event that justifies reopening
  that decision; absent it, the layered seam stands.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related

[0001](./0001-single-node-scope-pure-go-default.md),
[0004](./0004-content-addressable-blob-store.md),
[0007](./0007-optional-dense-arm.md),
[0008](./0008-polyglot-symbol-sidecar.md),
[0010](./0010-warm-serving-spine.md),
[0011](./0011-shard-level-freshness.md);
Protostar ADR 0002 (the context seam), Protostar DESIGN §12.
