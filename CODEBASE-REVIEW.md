# Codebase Review — moedex

**Generated:** 2026-08-17 · **Commit:** `5eafc62 (working tree; see note on concurrent development in summary)` · **Depth:** standard

## Summary

moedex's foundational retrieval engine is in good shape. The Cox regex-to-trigram reduction, the single most correctness-critical piece of logic in the repo, came back with zero findings after a dedicated adversarial pass; the frozen BM25 tokenizer stats and the freshly-parallelized graph-refresh worker pool were both independently verified race-free and logically equivalent to the code they replaced. Where the review found real problems, they cluster in two places worth prioritizing in this order. First, fix the two silent, high-consequence gaps in the privacy and correctness invariants the project states as absolute: a directory-scoped .ai-privacy.yml override silently protects nothing if an operator omits a trailing slash (F-02), the experimental LSP navigation arm bypasses the privacy policy across all three of its callers (F-03), and a literal query containing a raw newline byte produces a false cross-line match that violates the ripgrep-parity guarantee the whole engine exists to uphold (F-04) — all three are reproduced, all three are quiet failures with no error to alert anyone. Second, budget real time for the knowledge-graph layer: it is under active, fast-moving construction (three commits landed in the working tree during this review alone), and essentially every edge type it produces besides plain symbol references — LSP calls, semantic similarity, HTTP routes, manifest dependencies, and the brand-new type-hierarchy edges — shares one root cause, where the incremental-refresh code path and the full-build code path have quietly drifted out of sync despite an internal comment promising they're identical (F-01). Treat that whole cluster as one fix, not five.

A second, independent theme is worth calling out because it wasn't found by one lucky reviewer but by three who never saw each other's work: moedex's hand-rolled binary sidecar formats (the trigram index, the graph adjacency file, and the symbol sidecar) all trust a header-provided element count to size an allocation before checking it against the file's actual size, and one instance was reproduced consuming ~80GB of RAM from a single corrupted byte (F-05). That's a cheap, mechanical fix applied in three places using a pattern the codebase already uses correctly elsewhere. Every high-severity finding in this report survived an independent refutation attempt in some form — none were thrown out outright — but eighteen of twenty were adjusted downward in severity or narrowed in trigger once someone checked reachability against the real callers, production deploy paths, and systemd semantics, which is the expected and healthy outcome of that process rather than a sign the original reviews were sloppy. Two findings (H10, H18) turned out to already be dormant behind the graph-layer gap above; fixing that gap first will make both of them live again, so they're worth fixing in the same pass. Nothing in this review rises to critical once reachability is accounted for, and the two things that looked critical on first read (a missing graph sidecar crashing the daemon at boot, and the graph-edge-loss cluster) both turned out to need a specific, narrower precondition than 'runs in normal operation' — real bugs, worth fixing soon, but not pages.

## Findings at a glance

| Severity | Count | Meaning |
| --- | ---: | --- |
| Critical | 0 | Exploitable, destructive, or crashes a reachable path in normal operation. |
| High | 11 | Produces wrong behaviour, silent failure, or a leak on a realistic input. |
| Medium | 24 | Works today but is fragile — unguarded assumption, untested branch with real logic. |
| Low | 5 | Maintainability or clarity, no behavioural risk. |
| **Total** | **40** | |

| Lane | Critical | High | Medium | Low |
| --- | ---: | ---: | ---: | ---: |
| architecture | 0 | 0 | 2 | 1 |
| binary-format-safety | 0 | 1 | 2 | 0 |
| build-tag-boundary | 0 | 0 | 1 | 0 |
| correctness | 0 | 1 | 5 | 3 |
| graph-refresh-consistency | 0 | 1 | 1 | 1 |
| graph-refresh-consistency / resilience | 0 | 1 | 0 | 0 |
| performance | 0 | 0 | 1 | 0 |
| privacy-boundary | 0 | 1 | 0 | 0 |
| resilience | 0 | 4 | 8 | 0 |
| resilience / architecture | 0 | 0 | 1 | 0 |
| security | 0 | 1 | 0 | 0 |
| security / build-tag-boundary / privacy-boundary | 0 | 1 | 0 | 0 |
| testing | 0 | 0 | 3 | 0 |

## Remediation status

Tracks the codebase-review-fixes effort across every wave run against this report so far. All remediation work lands on `codebase-review-fixes/2026-08-18`.

| Wave | Findings in scope | Fixed | Skipped | Unresolved conflicts |
| --- | --- | ---: | ---: | ---: |
| high | F-01, F-02, F-03, F-04, F-05, F-06, F-07, F-08, F-09, F-10, F-11 | 11 | 0 | 0 |
| medium | F-12, F-13, F-14, F-15, F-16, F-17, F-18, F-19, F-20, F-21, F-22, F-23, F-24, F-25, F-26, F-27, F-28, F-29, F-30, F-31, F-32, F-35 | 21 | 0 | 1 |

**Totals so far:** 32 fixed · 0 skipped · 1 unresolved conflict, out of 40 findings. All 11 High-severity findings are fixed; 21 of 24 Medium-severity findings are fixed (1 unresolved conflict: F-24 on `review-fix/2026-08-18/unit-11`, needs manual merge; F-33 and F-34 have not yet had a remediation wave run against them); the 5 Low-severity findings have not yet had a remediation wave run against them.

## Coverage

| | |
| --- | --- |
| Source files | 151 |
| Source lines | 40690 |
| Chunks reviewed | 24 of 40 |
| Review passes | 39 |
| Lanes | correctness, security, resilience, performance, testing, architecture, privacy-boundary, binary-format-safety, build-tag-boundary, graph-refresh-consistency |

### Not covered

These were deliberately left out of scope by the depth budget. Nothing here has been cleared — it has been *skipped*.

- chunk-03 cmd/moedex-mcp/** (85 loc) — thin single-repo wiring; excluded by budget, low risk
- chunk-04 cmd/moedex-nav/** (361 loc) — CLI wrapper; core LSP logic covered via internal/navigate instead
- chunk-05 cmd/moedex-parity/** (263 loc) — thin CLI wrapper around internal/parity, which was covered
- chunk-07 cmd/moedex/** (60 loc) — trivial one-shot CLI; excluded, negligible risk
- chunk-08 cmd/scale/** (177 loc) — diagnostic sizing tool, not on the serving/query path
- chunk-10 internal/classify/** (557 loc, churn 1) — stable, low-churn framework classifier; excluded by budget
- chunk-11 internal/contextwin/** (491 loc, churn 5) — excluded by budget in favor of higher-churn chunks
- chunk-16 internal/fmindex/** (289 loc) — explicitly a deferred/research arm per ARCHITECTURE.md
- chunk-17 internal/fold/** (28 loc) — trivial helper
- chunk-18 internal/graph/candidates/** (813 loc, churn 5) — excluded by budget; read as a dependency by several graph-refresh reviewers but not independently reviewed
- chunk-19 internal/graph/cluster/** (194 loc) — excluded by budget
- chunk-21 internal/graph/httproute/** (2000 loc, churn 1) — stable, low-churn route-extraction package; excluded by budget despite its size
- chunk-23 internal/graph/verify/** (437 loc, churn 4) — excluded by budget; read as a caller dependency by graph-refresh reviewers
- chunk-24 internal/graph/{model} (119 loc) — trivial
- chunk-35 internal/setops/** (278 loc, churn 4) — pure-Go set-ops kernel; excluded by budget
- chunk-38 internal/trigram/** (16 loc) — trivial
- chunk-39 internal/version/** (70 loc) — trivial
- chunk-40 scripts/** (507 loc, churn 11) — install/deploy scripts; excluded by budget

---

## Findings

### High

#### F-05 — cmd/moedex-corpus has no signal handling; a kill mid-sync permanently wedges the hourly job

_`cmd/moedex-corpus/main.go:492` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `9d57f13` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** main.go uses context.Background() in every runXxx path with no os/signal handling anywhere. A kill (systemd stop, OOM, reboot) landing between the git add and git commit steps of a managed sync (internal/corpus/managed_sync.go) leaves the superproject index staged-but-uncommitted.

**Trigger.** SIGTERM/SIGKILL/OOM/reboot during the narrow window between `git add` and `git commit` in a managed sync -- realistic for an hourly, unattended systemd-timer job over a long-running deployment.

**Impact.** doctor's cleanliness check fail-closes with a manual-only fix, and since the shipped unit runs `doctor` as ExecStartPre, every subsequent hourly run fails at step one until an operator manually intervenes -- the corpus silently stops refreshing.

**Verification.** Confirmed; trigger is actually broader than originally scoped -- the whole git-mutation loop in managed_sync.go (lines 252-347), not just the add/commit boundary, and there is no systemd Restart= or auto-heal to recover from it.

**Fix.** Install an os/signal handler that cancels a context passed through to the git/glab calls and, at minimum, ensures managed_sync's add+commit sequence either completes or cleanly reverts the staged index on interruption.

#### F-06 — cmd/moedex-corpus has no context deadline on any subprocess call

_`cmd/moedex-corpus/main.go:182` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `9d57f13` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Every glab/git subprocess call inherits an undeadlined context.Background() straight through to exec.CommandContext, and the shipped moedex-sync.service unit sets no TimeoutStartSec.

**Trigger.** A stalled network call (dead VPN, hung GitLab connection) during the hourly sync.

**Impact.** The process blocks forever with no self-imposed bound; since the unit is Type=oneshot, later timer ticks won't spawn a second instance, so the corpus goes silently stale with no error and no non-zero exit to alert on.

**Verification.** Confirmed and found to be worse than originally scoped: verified against systemd.service(5) that a Type=oneshot unit disables the default TimeoutStartSec entirely unless explicitly set, so this unit has zero time bound at any layer -- there is no systemd-level mitigation to fall back on.

**Fix.** Wrap the top-level context with a deadline sized to the expected sync duration (or set TimeoutStartSec in the systemd unit) so a hang fails loudly instead of blocking indefinitely.

#### F-07 — No detection anywhere in the offline tooling for a stale (failed-to-rebuild) graph sidecar

_`cmd/moedex-index/main.go:219` · also at `cmd/moedex-index/doctor.go:252`, `internal/server/doctor.go:17,47` · lane: graph-refresh-consistency / resilience · confidence: confirmed_

**Status.** Fixed — commit `e839d22` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** build/refresh/cas-export intentionally downgrade a graph-sidecar build failure to a stderr warning and exit 0 regardless, and moedex-index doctor never checks the graph sidecar's presence or freshness -- it only tracks the dense embedding store's freshness.

**Trigger.** Any failure in the graph-build pass during `moedex-index build`/`refresh`. Total absence of the graph file is actually loud (it crashes moedex-serve -mcp/-mcp-http at boot, per the related finding on daemon boot hard-failing) -- the genuinely silent failure mode is a *stale* graph: a refresh that fails to rebuild an already-existing graph leaves the prior generation in place indefinitely, and SIGHUP reload just logs and keeps serving the old one.

**Impact.** An operator can run build/refresh/doctor and see all-green output while search_context's graph annotations are silently frozen at an old generation, with no counter or check anywhere surfacing the staleness.

**Verification.** Confirmed the core gap by reading the full doctor.go in both internal/server and cmd/moedex-index. Refined the impact: the finding's original 'silently absent' framing was only half right, since total absence is actually loud (crashes boot). The real silent case is staleness, not absence.

**Fix.** Have `moedex-index doctor` check for the graph sidecar's presence and freshness the same way it checks the embedding store, and/or make a graph-build failure surface as a non-zero exit code (or a distinctly-flagged warning) rather than an easily-missed stderr line.

#### F-03 — LSP navigation MCP tools bypass .ai-privacy.yml entirely

_`cmd/moedex-serve/nav_lsp.go:110` · also at `internal/navigate/pool.go:283 (root cause: package has no privacy hook at all)`, `cmd/moedex-nav/main.go (third unguarded caller)` · lane: security / build-tag-boundary / privacy-boundary · corroborated by: build-tag-boundary, security, privacy-boundary · confidence: confirmed_

**Status.** Fixed — commit `e513851` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** The -tags lsp MCP tools (find_definition, find_references, find_implementations, find_symbol, symbols_overview) accept a caller-supplied absolute file/root path and hand it directly to navigate.Pool, which spawns a real external language server rooted there, with no check against .ai-privacy.yml. internal/server/graphcalls_lsp.go DOES call ingest.LSPWorkspaceAllowed before touching the same Pool API, proving the check is meant to exist at this layer, but internal/navigate itself has zero awareness of the privacy policy and offers no hook for a caller to install one -- of the three current callers, only graphcalls_lsp.go gets it right; cmd/moedex-serve/nav_lsp.go and cmd/moedex-nav's CLI both pass paths straight through.

**Trigger.** Run moedex-serve built with `make install-dense` (-tags "onnx lsp") per the documented production deploy path (deploy/com.moedex.serve.plist, -mcp-http), then call find_definition/find_references with a root path inside a repo whose .ai-privacy.yml marks it Restricted (level 1).

**Impact.** An external language server is launched rooted at Restricted content and may scan the entire workspace (including files never indexed), directly contradicting the fail-closed privacy guarantee that governs every other content-reading path in the system, and matching the gap ADR 0021 already names as a known, not-yet-closed risk.

**Verification.** Confirmed unchanged. Checked whether the -tags lsp arm is only an unused experimental spike (which would lower urgency) and found it is not: make install-dense and the documented scripts/install-macos.sh production install path both build and deploy it, so the gap is live in a real deployment path, not a shelved prototype.

**Fix.** Add the ingest.LSPWorkspaceAllowed(root) check once inside navigate.Pool.NavigatorFor (or NewLSP) itself, before any process is spawned, so all three current and any future callers get the guarantee structurally instead of relying on each caller remembering it.

#### F-04 — Multiple hand-rolled binary sidecar formats size allocations from untrusted header counts with no upper bound

_`internal/graph/diskgraph/diskgraph.go:687` · also at `internal/diskstore/diskstore.go:538 (loadBlobs, numBlobs/numTrigrams)`, `internal/diskstore/dedupstore.go:195 (loadDedupedBlobs)`, `internal/symbol/codec.go:206 (readIndex, blobCount/symCount/nameLen/refCount)` · lane: binary-format-safety · corroborated by: binary-format-safety (x3 assignments) · confidence: confirmed_

**Status.** Fixed — commit `29a9856` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** In all three formats, a header-provided element count (blobCount/nameCount in diskgraph.go; numBlobs/numTrigrams in diskstore.go/dedupstore.go; blobCount/symCount/nameLen/refCount in symbol/codec.go) is read and used directly to size a make() map/slice before being cross-checked against the actual remaining bytes in the file's corresponding section. Every one of these formats already implements exactly this kind of bound-checking for OTHER count fields in the same file (diskgraph's node/edge sections, diskstore's per-record checkCount, contentstore's capHint clamp) -- this is a gap in an otherwise well-hardened pattern, not an absent pattern. Reproduced directly for diskgraph.go: a corrupted count field drove make(map[string]uint32, 4294967295) + make([]string, 0, 4294967295) to ~82GB RSS in ~32s with no panic.

**Trigger.** A corrupted or maliciously truncated corpus-graph.graph / *.idx / *.sym sidecar file with an inflated count field but an otherwise-valid-looking header, loaded at daemon boot, SIGHUP reload, or incremental graph refresh -- e.g. from a disk bit-flip, an interrupted write on a filesystem without the atomic temp+rename this repo otherwise favors, or a hand-edited file during testing.

**Impact.** diskgraph.go: reproduced ~82GB allocation attempt on Open(), which runs on every daemon boot/reload and every incremental refresh -- a real OOM/crash vector on corrupt input. symbol/codec.go: reproduced panic-worthy allocation that crashes the whole live-serving daemon during a SIGHUP reload (the only recover() in cmd/moedex-serve wraps HTTP handlers, not the reload goroutine), defeating the documented 'a failed reload keeps the current one' guarantee. diskstore.go: same class of risk on the primary retrieval index format.

**Verification.** Independently reproduced in a standalone program: 77.9GB peak RSS in 29.9s for the diskgraph.go case, closely matching the original reviewer's ~82GB/~32s. Confirmed diskgraph.Open is reachable on every daemon boot, on live SIGHUP reload (in a goroutine with no recover(), since recover() only wraps HTTP middleware), and during incremental refresh -- so a corrupted file can crash a fresh boot or take down an already-serving daemon, bypassing the 'keep the old graph on failed reload' fallback entirely.

**Fix.** Apply each package's own existing bound-checking idiom (diskstore's checkCount, contentstore's capHint clamp) to these count fields too: reject a count that implies more bytes than remain in its section before allocating.

#### F-08 — A directory-scoped privacy_levels override without a trailing slash silently protects nothing

_`internal/ingest/privacy.go:362` · lane: privacy-boundary · confidence: confirmed_

**Status.** Fixed — commit `f4e2501` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** privacyPathContains decides file-vs-directory purely from whether the policy author typed a trailing '/' in the path, with no filesystem check and no documentation anywhere (ADR 0021, ARCHITECTURE.md, or a code comment) warning that this matters. A directory-scoped override written as `path: /secrets` instead of `/secrets/` parses and validates without error, but since a directory is never itself a git blob path, the exact-match branch can never fire -- every file under that directory is silently treated as unrestricted. Every production build path (internal/blobstore, internal/parity, cmd/moedex-index, cmd/scale, internal/eval) shares this same logic via ingest.AIPrivacyFingerprint/ingest.Repo.

**Trigger.** An operator authors a per-path Restricted override in .ai-privacy.yml for a directory and omits the trailing slash -- an easy, silent, undetectable typo in hand-written YAML with no schema validation catching it.

**Impact.** This is the implementation of the single most safety-critical invariant CLAUDE.md names in the entire repository. A silently-ineffective directory-scoped restriction means Restricted content under that path gets indexed, embedded, and served to any agent querying the corpus over MCP -- exactly the confidentiality failure the whole policy system exists to prevent, with the policy author having every reason to believe it's working (no error, no warning, valid parse).

**Verification.** Reproduced live with a temporary test: a `privacy_levels` override of `path: /secrets` (missing trailing slash) parses and validates with zero error yet leaves `secrets/token.txt` unfiltered and indexed. Confirmed directory-scoped overrides are the core reason the privacy_levels feature exists in the first place (not just an edge case) -- ADR 0021 and the repo's own corpus-audit tests treat per-path overrides as a real, tracked, live category. Severity held at high, not escalated to critical since it requires an authoring mistake rather than being wrong by default, and not downgraded since the mistake is silent and easy to make.

**Fix.** Make privacyPathContains treat every override as `child == parent || strings.HasPrefix(child, parent+"/")` regardless of the trailing slash, since a tracked file can never have a descendant -- this is safe to widen unconditionally with no over-matching risk.

#### F-09 — The /mcp Streamable HTTP transport has no concurrency bound

_`internal/mcp/http.go:29` · also at `cmd/moedex-serve/main.go:392 (unwrapped) vs :560 (/search, wrapped in withConcurrencyLimit)` · lane: security · confidence: confirmed_

**Status.** Fixed — commit `4b6da4b` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Server.maxConcurrency (and its semaphore) exists only inside the stdio Serve() loop. HTTPHandler/handleHTTPPost/handleHTTPBatch dispatch every POST straight through handleSafe with no limiter, and handleHTTPBatch additionally has no cap on batch-array length and never checks ctx.Err() between items. cmd/moedex-serve/main.go explicitly wraps the sibling /search route in withConcurrencyLimit for precisely this CPU-bound-scan concern, but applies no equivalent wrapper to /mcp.

**Trigger.** Many concurrent agent sessions (the documented purpose of -mcp-http: 'let many agent sessions share one warm daemon concurrently') or a single client sending a large JSON-RPC batch array.

**Impact.** A realistic, no-precondition-required resource-exhaustion path against the CPU-bound ranking/context-assembly pipeline, with no existing test covering /mcp concurrency (concurrency_test.go only covers /search).

**Verification.** Confirmed unchanged. Found direct corroborating evidence: the README documents /search's concurrency guard as built specifically to 'mirror the MCP server's own request-concurrency guard,' confirming /mcp's HTTP transport lacking one is an unintentional gap rather than a considered design choice. -mcp-http is documented as the shared multi-session daemon, so the trigger is normal intended use.

**Fix.** Wrap the /mcp HTTP route in the same withConcurrencyLimit middleware /search already uses, and cap handleHTTPBatch's array length.

#### F-10 — A wedged (alive but unresponsive) language server can hang every lane sharing it, including Close()

_`internal/navigate/lsp.go:1019` · also at `internal/navigate/lsp.go:1051,736,1055,391`, `internal/navigate/pool.go:319,743` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `e513851` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** call()'s write happens before the context-aware select, writeMu is shared by every lane routed to that pooled server, and notify()/ensureFresh() take no context at all -- so a server that stops draining stdin while staying alive can wedge every lane sharing it indefinitely, defeat both production timeout layers (MCP's 30s default and moedex-nav's -timeout flag), and even hang Close() itself since the shutdown handshake goes through the same blocking path.

**Trigger.** A pooled language server process that stops reading its stdin (e.g. an internal deadlock or a slow/blocked filesystem operation inside the server) while remaining alive from the OS's perspective.

**Impact.** Every navigation request sharing that server hangs forever regardless of caller-side timeouts, and graceful daemon shutdown blocks indefinitely on Close(), leaving the child process unkilled.

**Verification.** Confirmed unchanged, including that daemon shutdown itself is genuinely unbounded (HTTP shutdown has a 5s cap but the deferred navigate Close() does not). Checked whether the -tags lsp arm's 'still a spike' framing lowers urgency and found it does not: ADR 0017 explicitly calls the pool's concurrency hardening 'productionized.'

**Fix.** Bound writeMu acquisition and the write itself by the caller's context, and give notify()/ensureFresh() a context/timeout too, so a wedged server's stdin write times out and triggers the existing restart-on-dead-server path instead of blocking forever.

#### F-11 — Literal's positional fast path can match a substring spanning two lines

_`internal/search/search.go:182` · lane: correctness · confidence: confirmed_

**Status.** Fixed — commit `8421c3d` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** LiteralWithStats's positional begin/end-gram intersection verifies a candidate with bytes.Equal against the blob's raw, unsplit content and attributes the match to a single line via b.LineOf(pos), without checking that the matched span stays within that line. A literal query containing a raw newline byte (length >= 3, so it takes the fast/positional path rather than the line-bounded fallback) can match content that straddles two real lines.

**Trigger.** A literal query containing a raw newline byte, reachable via cmd/moedex-serve's GET /search?q=... handler with a URL-encoded newline (%0A) in the query string, or any CLI one-shot query.

**Impact.** Reproduced directly: indexing "food\nbar\n" and querying "od\nb" via search.Literal returns a bogus single-line match on line 1 -- a false-positive result ripgrep itself refuses to even attempt (rg -F errors on a literal newline outside multiline mode), violating the core ripgrep-parity invariant this repo is built around. The equivalent short-query fallback path in the same file (which line-splits first) correctly returns nothing for the analogous case, so the two paths in this one file disagree with each other and with ripgrep.

**Verification.** Reproduced directly: indexed 'food\nbar\n', queried the literal 'od\nb' via search.LiteralWithStats, got a false match at line 1 that no single line actually contains. Confirmed the short-query fallback path correctly returns zero for the analogous case, and confirmed the HTTP /search?q= handler and both CLI one-shot paths pass the query through unmodified, with a URL-encoded newline (%0A) surviving net/url decoding as a raw byte.

**Fix.** Bound the match check to the single line via b.LineAt(pos) -- already used elsewhere in this file for exactly this purpose -- before accepting a positional-path candidate.

#### F-01 — Incremental (and default-build full) graph refresh omits most edge types entirely

_`internal/server/graphrefresh.go:124` · also at `internal/server/graphbuild.go:188,198`, `internal/server/graphhierarchy.go:118,364`, `internal/server/graphcalls_lsp.go`, `internal/server/graphsimilar_onnx.go`, `cmd/moedex-index/graph_onnx.go:37` · lane: graph-refresh-consistency · corroborated by: build-tag-boundary, correctness, graph-refresh-consistency (x4 assignments) · confidence: confirmed_

**Status.** Fixed — commit `f9049e7` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** RefreshGraph's incremental carry-forward logic (graphrefresh.go:124) only preserves an existing edge if its Name is present in sweep.eligible, which by construction can never contain the empty string. LSP CALLS, ONNX SIMILAR_TO, HTTP_CALLS, manifest DEPENDS_ON, and the newly-added EXTENDS/IMPLEMENTS/CONTAINS_METHOD hierarchy edges are all persisted with Name == "" (they have no associated symbol name), so every one of these edge families is unconditionally treated as stale and dropped the moment any single blob anywhere in the corpus changes. Separately, RefreshGraph's own rebuildAll fallback (the only full-build code path the default, non-onnx moedex-index binary ever calls) never invokes the LSP-call, ONNX-similarity, HTTP-route, manifest-dependency, or hierarchy edge-generation passes at all -- those only run inside BuildGraphWithOptions, whose only production caller is the onnx-tagged cmd/moedex-index/graph_onnx.go path.

**Trigger.** Only fires when RefreshGraph's delta path actually runs against a previous graph that already contains Name=="" edges. It does NOT fire for the default (non-onnx, non-lsp) build at all, since that binary's wrapper only ever calls RefreshGraph and its full-build fallback (rebuildAll) never generates HTTP/manifest/hierarchy edges in the first place -- there is nothing to drop. It also does NOT fire for an onnx-tagged binary under default settings, since MOEDEX_GRAPH_SIMILAR_TOP_K defaults nonzero and that always routes every build/refresh through the full (non-incremental) BuildGraphWithOptions path. It fires when: an onnx-tagged deployment runs a build/refresh with similarity enabled (topK>0, producing SIMILAR_TO/HTTP/manifest/hierarchy edges), then later runs a refresh with MOEDEX_GRAPH_SIMILAR_TOP_K=0 (or any caller invokes server.RefreshGraph directly, as only tests do today) against that same shard dir -- every edge of those types is silently and permanently dropped on that refresh and stays gone on all subsequent refreshes, with no error and no counter surfaced to an operator who isn't specifically watching EdgesDropped in the stats output.

**Impact.** The knowledge-graph MCP tools (trace_calls for LSP-derived calls, trace_consumers/impact_analysis for HTTP routes and manifest dependencies, trace_hierarchy for type hierarchy, similar_to neighbors) silently return incomplete or empty results for entire categories of relationships, with no error, no warning, and no counter indicating anything is missing -- an agent querying "what depends on this file" or "what implements this interface" gets a confidently wrong (too-small) answer that looks like a complete one.

**Verification.** Confirmed as a real, currently-untested code defect by independent trace, but reachability is much narrower than first scoped. The default (non-onnx/lsp) build never generates HTTP/manifest/hierarchy edges in the first place (rebuildAll never calls those passes), so there is nothing to drop there. An onnx-tagged build under its own default settings (MOEDEX_GRAPH_SIMILAR_TOP_K unset, defaulting nonzero) always takes the full BuildGraphWithOptions path too, so the buggy incremental path is not exercised by default even there. The bug only fires when an operator explicitly sets MOEDEX_GRAPH_SIMILAR_TOP_K=0 on a refresh following an earlier build/refresh that had it enabled, or when a caller invokes server.RefreshGraph directly against a graph built by BuildGraphWithOptions (only tests do this today). Downgraded from critical to high on that basis.

**Fix.** Give every edge type a stable identity key usable by the incremental carry-forward/dirty-check logic instead of overloading the symbol Name field (e.g. key on (Type, Source, Target) or a synthetic identity string), and make RefreshGraph's rebuildAll path call the same addLSPCallEdges/addSimilarToEdges/addHTTPCallEdges/addManifestEdges/addHierarchyEdges passes that BuildGraphWithOptions runs, so the two paths are actually equivalent as their shared doc comment already claims.

#### F-02 — A missing or unbuilt graph sidecar hard-fails the entire moedex-serve MCP daemon boot

_`internal/server/graphtools.go:137` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `c4ccdb6` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** openGraphSnapshot treats a missing/unbuilt corpus-graph.graph file as a fatal boot error (propagating to os.Exit(1)) for moedex-serve -mcp/-mcp-http, even though every sibling ranking sidecar (token index, symbol index, embedding store) is documented and implemented as best-effort ("a failed cache write never fails a boot"), moedex-index build/refresh itself treats a graph-sidecar build failure as a non-fatal warning, and this exact toolset's own SIGHUP Reload path treats the identical failure as recoverable (keeps serving the old graph).

**Trigger.** Start moedex-serve -mcp or -mcp-http against ANY shard directory whose corpus-graph.graph is absent -- which is not limited to a broken/older toolchain or manual deletion. It also includes: (a) a shard dir built or refreshed with today's moedex-index where the graph pass (best-effort per cmd/moedex-index/main.go:201-224) hit an internal error and only logged a stderr warning while build/refresh/cas-export still exited 0; and (b) the first deployment of this still-new graph feature's moedex-serve binary against any pre-existing production shard dir that predates it -- a scenario the docs never call out and moedex-index doctor never checks for. Severity is 'high' rather than 'critical' because the crash requires this specific (if realistic and, for an initial feature rollout, near-inevitable) precondition rather than firing on every normal boot once the sidecar exists and is kept current.

**Impact.** The entire MCP daemon refuses to start and search_context / all graph tools become completely unavailable, rather than degrading to graph-less search results the way every other optional sidecar degrades.

**Verification.** Confirmed the hard-fail-on-missing-graph behavior and its inconsistency with the sibling sidecars, SIGHUP reload, and the offline builder's own best-effort policy. Downgraded from critical to high because it needs the specific precondition of a missing/failed sidecar rather than firing on every normal boot.

**Fix.** Make daemon boot treat a missing/unbuildable graph sidecar the same way SIGHUP reload and moedex-index build already do: log a warning and serve without graph annotations, rather than exiting.

### Medium

#### F-21 — SIGHUP reload skips the graph-sidecar refresh entirely whenever the ranked-corpus rebuild fails

_`cmd/moedex-serve/main.go:288` · also at `cmd/moedex-serve/main.go:421` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `81b3b85` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** In both runMCP's and runMCPHTTP's SIGHUP handler, `graphTools.Reload(shardDir)` is only called if `server.OpenRank` succeeds; on any OpenRank error the handler does `continue` before ever attempting the graph reload. The two subsystems (rankHolder/RankCorpus and GraphToolset) are independently refcounted and hot-swappable — there is no structural reason a rank-corpus rebuild failure should block an unrelated, otherwise-successful graph-sidecar refresh.

**Trigger.** A SIGHUP arrives while the shard dir has a valid, refreshed graph sidecar (built via `moedex-index build_graph_once`/refresh) but `server.OpenRank` fails for any reason (e.g. the configured dense/embedding backend is transiently down — an explicitly anticipated failure mode per the surrounding comments).

**Impact.** The graph tools (trace_calls/trace_consumers/impact_analysis, plus the graph annotations fused into every search_context result via mcp.WithGraphAnnotator) stay pinned to the old graph generation with no attempt to pick up the new one, and no log line even mentions that the graph reload was skipped — the operator only sees the rank-corpus failure message and has no way to know the graph refresh never ran. This can persist for an arbitrary number of subsequent failed cycles.

**Fix.** Attempt `graphTools.Reload(shardDir)` unconditionally (or at least independently of the OpenRank outcome) so a rank-corpus failure does not gate the separately-refcounted graph reload; log the two outcomes independently the way the metrics/log lines already do for the rank corpus.

#### F-22 — SIGHUP reload has no dense-arm degrade-to-lexical fallback, unlike boot, so a transient embed-service hiccup aborts the whole refresh

_`cmd/moedex-serve/main.go:190` · also at `cmd/moedex-serve/main.go:288`, `cmd/moedex-serve/main.go:421` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `81b3b85` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** openRankCorpus (used only at process boot) explicitly retries `server.OpenRank` with `cfg.Emb = nil` if the first attempt fails while dense was configured ("Dense build failed (service down, bad model, etc.): degrade to lexical"). The SIGHUP reload handlers in runMCP and runMCPHTTP call `server.OpenRank(ctx, shardDir, cfg)` exactly once with the boot-resolved `cfg` and, on any error, just log-and-`continue` — they never reuse the same degrade-to-lexical retry that boot has.

**Trigger.** The dense arm was healthy at boot (cfg.Emb set), then the configured embedding backend (MOEDEX_EMBED_URL / ONNX runtime) becomes transiently unavailable exactly during a scheduled SIGHUP-triggered refresh (the corpus-refresh workflow described in the package doc is exactly this: build embeddings out of band, then SIGHUP).

**Impact.** A refresh cycle that could have succeeded lexical+symbol(+graph) is instead abandoned wholesale, and the daemon keeps serving the previous (now stale) generation until a later SIGHUP happens to land while the embed backend is healthy again. This is strictly less resilient than boot's own handling of the identical failure mode.

**Fix.** Factor the boot-time degrade-to-lexical retry (currently only inside openRankCorpus) into a helper both the initial open and the SIGHUP reload paths call, so a reload-time dense failure falls back to lexical+symbol the same way a boot-time one does instead of aborting the whole reload.

#### F-23 — SIGHUP reload's graph-vs-corpus failure handling is inconsistent and mislabeled: it swaps in a new corpus generation even when the paired graph reload fails, and logs the wrong failure when it doesn't

_`cmd/moedex-serve/main.go:293` · also at `cmd/moedex-serve/main.go:427`, `internal/server/graphtools.go:338-352` · lane: resilience / architecture · corroborated by: chunk-06__resilience, chunk-34__architecture · confidence: confirmed_

**Status.** Fixed — commit `81b3b85` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** `GraphToolset.Reload` (internal/server/graphtools.go:338-352) swaps `g.cur` to the newly-opened generation FIRST, and only afterward waits for the old generation's readers and calls `old.close()`, returning that close's error. So a non-nil return from Reload does not mean the reload failed — the new graph is already live — it means releasing the PREVIOUS generation's mmap/symbol resources failed. Both SIGHUP handlers in main.go log this as `"graph reload failed (%v); keeping current graph"`, which is false: the graph has already been swapped. Separately: In both SIGHUP handlers, when server.OpenRank fails the code correctly `continue`s and leaves everything on the old generation. When graphTools.Reload fails immediately afterward, the code only logs ('graph reload failed ...; keeping current graph') and falls through to `holder.swap(nrc)` anyway — swapping in the new RankCorpus generation (new blob content, potentially new/changed file SHAs) while GraphToolset keeps serving its old generation's node/byPath catalog built from the previous corpus content.

**Trigger.** `old.close()` (diskgraph.Graph.Close + SymbolCorpus.Close, both backed by syscall.Munmap) errors on a SIGHUP reload — e.g. a munmap failure on the outgoing generation. Also: A SIGHUP reload where server.OpenRank(ctx, shardDir, cfg) succeeds but graphTools.Reload(shardDir) fails on the same shard directory — e.g. the graph sidecar (corpus-graph.graph) is transiently missing, mid-write, or corrupt while the shard set itself is valid. Given the graph layer is explicitly under active, phased construction right now (new cmd/moedex-index/build_graph_once.go and build_graph_tool.go in this working tree suggest graph-sidecar generation is being decoupled into its own step), a skew between when shards refresh and when the graph sidecar is regenerated is a realistic near-term operational sequence, not just a hypothetical.

**Impact.** The operator's log tells them the reload did not take effect and the daemon is still serving the previous graph generation, when in fact the NEW generation is already being served for every trace_calls/trace_consumers/impact_analysis call and every search_context graph annotation. This misdirects incident response (they may retry SIGHUP or assume the new graph data never went live) while masking the real problem, which is a resource leak in the retired generation. Additionally: search_context's graph-fused annotations (mcp.WithGraphAnnotator, wired unconditionally and on by default unless a caller explicitly passes graph_depth=0) would report neighbors/edges computed from stale blob content for files whose content changed in the just-swapped corpus generation, with no field anywhere in GraphQueryResult or BlockNeighbors (internal/mcp/mcp.go) indicating the graph data is from an older generation than the search hit it's attached to. This is a silent staleness, not a crash — the agent has no way to distinguish it from a fresh annotation.

**Fix.** Have GraphToolset.Reload distinguish the two failure sources (return a distinct error/flag for a close-after-swap failure vs. an open failure that genuinely didn't swap), and have main.go's log lines say "graph reload succeeded but releasing the previous generation failed" for the former instead of "keeping current graph". Additionally: Make the two reload steps consistent-by-construction: either (a) abort the whole reload cycle (mirroring the OpenRank failure branch's `continue`) when graphTools.Reload fails, so the corpus and graph generations always move together, or (b) if independent staleness is intentionally acceptable, thread a generation/version marker through GraphQueryResult and BlockNeighbors so a caller can tell the graph data is stale relative to the corpus it's attached to.

#### F-24 — corpusSnapshot.retire() and rankSnapshot.retire() silently discard the outgoing generation's Close() error

_`cmd/moedex-serve/reload.go:34` · also at `cmd/moedex-serve/reload.go:79` · lane: resilience · confidence: likely_

**Status.** Fixed on `review-fix/2026-08-18/unit-11`, unmerged — conflicts with unit-10's changes to `cmd/moedex-serve/reload_test.go` (both units add test content to the same file); needs manual merge.

**Problem.** `func (s *corpusSnapshot) retire() { s.wg.Wait(); _ = s.c.Close() }` and the equivalent `rankSnapshot.retire()` throw away the error from Close(). Corpus.Close()/RankCorpus.Close() can genuinely return a non-nil error (they propagate syscall.Munmap's return value from internal/diskstore's mmapRegion.Close), and unlike the reload build failure path (which increments `moedex_reloads_total{result="fail"}` and logs), a retire-time Close failure produces no log line and no metric.

**Trigger.** Munmap on the retired generation's shard(s) or shared content store fails (e.g. an OS-level munmap error) during a SIGHUP-triggered hot-swap — the same repeated-SIGHUP refresh pattern the daemon is designed around over its long-running lifetime.

**Impact.** The retired generation's mmap mapping and/or file descriptor is never actually released (Close's early-return-with-error still nils out the internal fields, so a second Close() call becomes a silent no-op even though the underlying resource was never unmapped), and there is zero operator-visible signal that this happened. Over many reload cycles on a long-lived daemon this is an unbounded, invisible address-space/fd leak.

**Fix.** Log (and optionally count via a new metrics label, e.g. `moedex_reloads_total{result="retire_fail"}`) the error returned by `s.c.Close()` / `s.rc.Close()` inside retire(), mirroring how the reload-build failure path is already logged and counted.

#### F-12 — blobstore.Store.Get never verifies retrieved content against its SHA key

_`internal/blobstore/blobstore.go:229` · also at `internal/blobstore/compact.go (bakes unverified content into a fresh pack)` · lane: binary-format-safety · confidence: confirmed_

**Status.** Fixed — commit `8407679` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Unlike the sibling MOECONT1 served content store (self-verifying by default via OpenContentStoreVerified, per ADR 0004's explicit two-silent-failure-risk guard), the CAS's Store.Get never re-hashes returned content against its claimed SHA. Content-region corruption in blobs.pack that leaves the length framing intact is silently returned as valid.

**Trigger.** Bit-rot or partial disk corruption in blobs.pack's content region that doesn't disturb the record length framing.

**Impact.** Corrupted content propagates unguarded through the default (non-deduped) cas-export path, and cas-compact even bakes it permanently into a fresh pack while discarding the recoverable pre-compaction backup -- CompactCAS's own liveness check only calls Has() (map membership), providing no content-integrity protection.

**Verification.** Confirmed the gap is real, but downgraded from high to medium: the production pipeline is exclusively the deduped format, which IS protected end-to-end by the sibling content store's self-verifying open (loud boot failure on corruption, not silent wrong results). The silent-corruption exposure is confined to the manually-invoked, non-default `cas-export` bridge that the managed refresh path never uses.

**Fix.** Add the same opt-out-able re-hash-at-read (or re-hash-at-open) verification the sibling content store already has, gated the same way (e.g. a comparable MOEDEX_VERIFY_CONTENT-style default-on flag).

#### F-26 — Open() treats an index with existing entries but a missing pack file as a fresh empty store instead of detecting corruption

_`internal/blobstore/blobstore.go:114` · also at `internal/blobstore/blobstore.go:195-198 — Has() only checks map membership, so it reports true for entries whose backing file is gone`, `internal/blobstore/compact.go:132-137 — CompactCAS's 'every live SHA is in the source pack' SACRED check calls Has(), so it passes even when the pack is actually missing` · lane: binary-format-safety · confidence: confirmed_

**Status.** Fixed — commit `8407679` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Open() first calls loadIndex(), which fully parses blobs.idx into s.byID/s.order/s.packPos (with correct bounds-checking against the index's own packBytes field). It then os.Stat()s blobs.pack; in the `os.IsNotExist(err)` branch it treats this as simply "fresh store" and falls straight through to creating a new pack via O_CREATE, with no check that len(s.byID) (or s.packPos) is already non-zero from the index it just loaded. The parallel case a few lines below (`fi.Size() < s.packPos`) DOES correctly treat a too-short pack as corrupt, but the symmetric case — pack completely absent while the index claims blobs — is not checked at all.

**Trigger.** blobs.idx is present and references one or more blobs (e.g. from a prior successful Flush/Close), but blobs.pack has been deleted or is missing from casDir at Open time — for example an operator manually removing what looks like a large redundant file, a restore/copy that brings back blobs.idx but not blobs.pack, or any directory-level tooling that doesn't treat the pair atomically the way this package's own writeIndex/Flush do.

**Impact.** Open() succeeds and reports a populated store (Len(), SHAs(), Has() all reflect the stale index) even though there is no backing content at all. CompactCAS's SACRED liveness check (compact.go:132-137) is defeated because it only calls Has(), so compaction proceeds past that guard and only fails later, deep inside the copy loop, when src.Get() hits an io.EOF on the freshly-created empty pack file — a confusing low-level I/O error rather than an immediate, clear 'index references N blobs but pack is missing' error at Open() time. No memory-unsafe access or wrong-content result occurs (Get ultimately does error out), but the failure is deferred and misleading, and any future caller that trusts Has() as a sufficient existence check (as CompactCAS's own SACRED comment implies it should be) will not get the protection that check is documented to provide.

**Fix.** In the `os.IsNotExist(err)` branch of Open() (blobstore.go:114-115), check `if len(s.byID) > 0` (equivalently s.packPos > 0) before accepting it as a fresh store, and return an error such as `fmt.Errorf("blobstore: index references %d blobs but pack file is missing (corrupt store)", len(s.byID))` — mirroring the existing `fi.Size() < s.packPos` corruption error immediately below it.

#### F-13 — Full/forced CAS export writes directly into the live shard directory with no staging+swap

_`internal/blobstore/export_deduped.go:70` · also at `internal/blobstore/export.go`, `internal/blobstore/export_shared.go` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `12fd4d5` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** ExportShardDir/ExportDedupedShardDir write shard files and the manifest directly into the live target directory with no staging-dir/atomic-swap, unlike every other rewriting operation in this package (RefreshDedupedShardDir, CompactCAS, CompactDedupedShardDir), all of which have a matching RecoverInterruptedXXX. Worse, ExportDedupedShardDir writes manifest.json (the documented completeness marker) before blobs.dat, inverting the package's own stated ordering rule. The documented `cas-export -deduped -force` workflow deletes the live dir up front.

**Trigger.** Process killed (or the machine loses power) during `moedex-index cas-export -deduped -force` after the pre-export deletion of the existing directory but before the new export completes.

**Impact.** The shard directory ends up with neither a valid old export nor a complete new one, and -- unlike every sibling rewrite operation in this package -- there is no automatic recovery path; a manual full re-export is required, and moedex-serve cannot start against the corrupted directory in the meantime.

**Verification.** Confirmed the ordering/no-staging bug is real, but downgraded from high to medium: reachable only via the manual, opt-in `-force` flag -- the actual scheduled path (internal/corpus/reindex.go, scripts/refresh-corpus.sh) always uses the crash-safe delta-refresh path instead. The CAS itself is untouched, so recovery is just re-running the export command.

**Fix.** Export to a temporary sibling directory and atomically rename it into place on success, matching the pattern already used by RefreshDedupedShardDir/CompactCAS/CompactDedupedShardDir; write blobs.dat before manifest.json to preserve the package's own completeness-marker ordering.

#### F-14 — No mutual exclusion around managed-corpus sync/init allows two concurrent invocations to desync the lockfile

_`internal/corpus/managed_sync.go:218` · also at `internal/corpus/managed.go:42` · lane: resilience · confidence: likely_

**Status.** Fixed — commit `8da5481` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** SyncManaged/InitManaged run a long check-then-act sequence (load lock, check working-tree cleanliness, compute a plan, run many git mutations, atomically rewrite corpus.lock.json, then git commit) with no lock file, flock, or pidfile anywhere in the package or its cmd/moedex-corpus driver to prevent two concurrent sync/init invocations against the same corpus root. The package's own doc comment ("mutations are serialized below that boundary") only holds within a single process.

**Trigger.** An operator manually running the raw moedex-corpus binary while a timer-driven sync is already in flight (bypassing systemctl). Downgraded from the original 'two overlapping scheduler ticks' framing since systemd job-merges a second `start` against an already-active oneshot unit, making routine timer-driven overlap impossible; only a manual bypass produces real concurrency.

**Impact.** Can commit a superproject snapshot where corpus.lock.json -- 'the exact acquisition snapshot consumed by indexing' -- disagrees with the actual gitlinks/.gitmodules, silently dropping or duplicating a project from what gets indexed until an operator happens to run doctor and manually recovers.

**Verification.** Confirmed the missing-lock gap and a traced silent-corruption mechanism (racing `git reset --hard` on a shared submodule) are real, but the trigger is an unusual precondition (manual bypass of systemctl) rather than routine operation. Better characterized as resilience/concurrency than security since no trust boundary is crossed.

**Fix.** Take a cross-process advisory lock (flock on a lockfile in the corpus root) around the full check-then-act sequence in SyncManaged/InitManaged, matching the durability discipline already applied to the CAS pack/index writes.

#### F-15 — LoadBlobs/LoadBlobsDeduped read the entire shard file just to use its small blob-section prefix

_`internal/diskstore/diskstore.go:301` · also at `internal/diskstore/dedupstore.go:281 (LoadBlobsDeduped)` · lane: performance · confidence: confirmed_

**Status.** Fixed — commit `6bbb324` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Both functions os.ReadFile the entire shard file and only then slice off the small blob-section prefix, discarding the postings section they just read -- directly contradicting their own doc comments ("skips the postings section entirely," "never pay[s] the positional-postings heap cost"). This runs once per shard on every OpenRank/BuildSidecars call: daemon boot, SIGHUP reload, and every moedex-index build/refresh. In the deduped format it's worse, since the blob section is tiny by design, so nearly the whole file gets pointlessly read and heap-copied.

**Trigger.** Any daemon boot, reload, or offline build/refresh against a shard directory with non-trivial posting-list content -- i.e. every normal production shard.

**Impact.** Every OpenRank call pays a full-file read-and-copy cost the code's own documentation says it deliberately avoids, scaling with total corpus size rather than blob-section size, on a hot path (boot latency, reload latency).

**Verification.** Confirmed the doc-comment contradiction verbatim and that it's reachable on every real boot/reload/build path. Downgraded from high to medium because it's a once-per-shard boot-time cost (never per-query), substantially mitigated by OS page cache in the two dominant call sites, and the oversized buffer isn't pinned in memory afterward.

**Fix.** Read only the blob-section byte range (blobOff to postOff, per the format's own header) instead of the whole file, matching what the doc comments already promise.

#### F-27 — onnx_disabled.go has no stub for NewONNXEmbedderFromFiles, breaking the documented twin-pair contract

_`internal/embed/onnx_disabled.go:1` · also at `internal/embed/onnx.go:102 — the real NewONNXEmbedderFromFiles that has no counterpart`, `ARCHITECTURE.md:70 — documents the exported surface as "ONNXEmbedder, NewONNXEmbedder, NewONNXEmbedderFromFiles (real only under -tags onnx; a no-op stub otherwise)"` · lane: build-tag-boundary · confidence: confirmed_

**Status.** Fixed — commit `80a07b2` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** onnx.go (built with -tags onnx) exports NewONNXEmbedderFromFiles as part of the package's public surface, but onnx_disabled.go (the !onnx twin) only stubs NewONNXEmbedder, ONNXEmbedder, Embed, Dim, and Close. NewONNXEmbedderFromFiles simply does not exist in the default build. This contradicts ARCHITECTURE.md's own description of the package's exported surface, which explicitly claims a no-op stub exists for it "otherwise" (i.e. in the non-onnx build), and breaks the exact-twin-pair pattern this lens is meant to enforce.

**Trigger.** Any future code added to a file without a `//go:build onnx` constraint that calls embed.NewONNXEmbedderFromFiles (e.g. a CLI flag to A/B a custom encoder path, as research/code-embedders.md and docs/adr/0007 suggest is the intended extension point) will fail to compile in the default build with "undefined: embed.NewONNXEmbedderFromFiles". Today this is latent: the only three call sites (internal/eval/gold_onnx_test.go:220, and the analogous NewONNXEmbedder call sites in cmd/moedex-index/graph_onnx.go, internal/server/graphsimilar_onnx_test.go, internal/eval/gold_densegate_test.go) are all themselves guarded by `//go:build onnx`, so `go build ./...` and `make health` currently pass clean.

**Impact.** No live runtime bug today (verified `go build ./...` and `go vet -tags onnx ./internal/embed/...` both succeed cleanly). The risk is architectural: the twin-pair guarantee CLAUDE.md/ARCHITECTURE.md rely on for this exact package (the template every other optional arm like lsp/simd is supposed to follow) is not actually held for the full exported surface, and the API-surface table in ARCHITECTURE.md is inaccurate for NewONNXEmbedderFromFiles specifically. A contributor who trusts that documentation and writes a tag-agnostic caller would get a confusing default-build compile break rather than the graceful `errNoONNX` they'd expect from the rest of the package.

**Fix.** Add a matching stub to internal/embed/onnx_disabled.go: `func NewONNXEmbedderFromFiles(runtimePath, modelPath, tokenizerPath string, inputNames []string, dim, maxSeq int) (*ONNXEmbedder, error) { return nil, errNoONNX }`, mirroring the existing NewONNXEmbedder stub, and either correct ARCHITECTURE.md if the omission was in fact intentional.

#### F-28 — A zero-relevant-doc GoldQuery silently dilutes every Report mean with no validation guard

_`internal/eval/runner.go:254` · also at `internal/eval/metrics.go:79-98 — RecallAtK returns 0 when numRelevant==0`, `internal/eval/metrics.go:171-189 — NDCGAtK returns 0 when idealDCGAtK==0`, `internal/eval/metrics.go:268-293 — UDCGAtK returns 0 when idealDCGAtK==0 (even if distractors are present and retrieved)`, `internal/eval/metrics.go:42-48 — NewBinaryGold(query) with no docs is a valid call that produces an empty Relevant map`, `internal/eval/reranker_train.go:114-137 — evaluateCrossVal has the identical unconditional per-query append/mean pattern` · lane: testing · confidence: likely_

**Status.** Fixed — commit `6cb41b1` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** finalizeMeans (runner.go:254-271) sums every QueryReport's metric values into the Mean* fields and divides by len(rep.Queries) unconditionally. Every per-query metric function in metrics.go returns exactly 0.0 for a query whose Relevant map has no grade>=1 entry (numRelevant==0 / idealDCGAtK==0). Nothing skips such a query, nothing warns about it, and NumRelevant (already computed per query at runner.go:238) is never used to filter or flag it.

**Trigger.** A GoldQuery with an empty (or all-negative/hard-distractor-only) Relevant map reaches Runner.Evaluate or evaluateCrossVal — e.g. a future call to the public NewBinaryGold(query) that omits the relevant-doc varargs (a one-argument typo), or a copy/paste slip when adding a new query to CorpusGold()/FixtureGold()/TCSslApiGold() that leaves the Relevant map empty. I checked all 111 hand-authored queries across gold_corpus.go, gold_fixture.go, and gold_tcsslapi.go and confirmed none currently has an empty Relevant map, so this has not fired yet.

**Impact.** If such a query is ever added, Evaluate() folds a guaranteed 0.0 into MeanRecall/MeanPrec/MeanMRR/MeanNDCG/MeanUDCG with the same weight as a real, answerable query, and nothing distinguishes 'the ranker failed an answerable query' from 'this query carries no gold label at all.' Because every absolute-floor gate in this package (TestCorpusGoldGate's minLexNDCG/minFullNDCG/etc., TestCorpusMeasurement's MeanMRR>=0.25) reads these same Mean* fields, either adding or later removing such a degenerate query shifts every reported mean without any accompanying signal — the scoreboard's job (catching a real ranking regression) becomes harder to trust exactly when a gold-set edit and a ranking change land together. This is the same class of problem the package already guards against one level up: runner_corpus.go's BuildPooledIndex silently drops an entire repo's files on ingest failure (perRepo[r.Repo] = 0; continue), and TestCorpusMeasurement explicitly asserts every perRepo count is nonzero for exactly this reason — but no equivalent per-query assertion exists.

**Fix.** Either (a) exclude queries with QueryReport.NumRelevant == 0 from the Mean* accumulation/denominator in finalizeMeans (the conventional trec_eval treatment of unjudged queries), or (b) add a guard mirroring the existing `for repo, c := range perRepo { if c == 0 { t.Errorf(...) } }` pattern in gold_corpus_test.go's TestCorpusMeasurement — e.g. a small helper (or a test) that fails loudly if any GoldQuery in CorpusGold()/FixtureGold()/TCSslApiGold() has NumRelevant == 0, so a future authoring mistake can't enter the aggregate silently.

#### F-16 — Manifest dependency resolution silently drops contested candidates and is order-dependent

_`internal/graph/manifest/resolve.go:323` · lane: graph-refresh-consistency · confidence: confirmed_

**Status.** Fixed — commit `c8bdff4` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** lookupProject's escaped-ProjectReference tier (used when a <ProjectReference> path walks above its own repo root to name a sibling repo by directory basename) returns the first repo in b.reposByDir[first] with a matching project file, instead of aggregating all matches like every other lookup tier in the file. If two repos share a base directory name and both have a matching project file, the losing repo's candidacy is silently dropped rather than emitted and marked Contested (violating the file's own explicit no-silent-recall-loss design principle), and the winner is decided by Add()-call order -- an artifact of shard/blob iteration order in the caller -- rather than by content.

**Trigger.** Two repos in the corpus sharing a base directory name, both containing a project file at an escaped ProjectReference's resolved sub-path -- reachable only via the onnx-tagged BuildGraphWithOptions path (the default build never calls addManifestEdges at all, per the related graph-edge-loss finding).

**Impact.** A real dependency edge is silently and non-deterministically dropped (order-dependent on shard/blob iteration) instead of being surfaced as Contested the way the file's own design intends, and a full rebuild vs. any future incremental resolution of the same input could disagree on which repo wins.

**Verification.** Confirmed the logic bug and its inconsistency with the file's other lookup tiers (which coalesce collisions via plain map append, this one doesn't). Downgraded from high to medium because it is provably unreachable in the default build today.

**Fix.** Make this tier aggregate all matches across reposByDir the same way the other lookup tiers already do, marking multiple matches Contested instead of returning the first one found.

#### F-29 — AddFile on a Restore(nil)/RestoreLazy-built Index either panics or silently orphans new postings

_`internal/index/index.go:197` · also at `internal/index/index.go:224 — Postings() always prefers ix.pp over ix.postings when pp is set`, `internal/index/index.go:251 — PostingCount() has the same pp-always-wins priority`, `internal/index/snapshot.go:61 — Restore(blobs, postings) aliases postings directly, including nil`, `internal/index/snapshot.go:76 — RestoreLazy(blobs, pp) leaves ix.postings as the empty map from New()` · lane: correctness · confidence: likely_

**Status.** Fixed — commit `2d4d9fe` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** addFile() unconditionally writes new postings into ix.postings (`ix.postings[t] = append(...)`, line 197), but Index.Postings()/PostingCount()/Trigrams() all check ix.pp first and return early when it is set, never consulting ix.postings. Restore(blobs, nil) — used by internal/server/rankcorpus.go:243 and symbolcorpus.go:123 — leaves ix.postings literally nil, since Restore overwrites New()'s empty map with whatever the caller passed. RestoreLazy (used by diskstore.LoadMmap, the production mmap-serving path) sets ix.pp and leaves ix.postings as an empty, never-consulted map.

**Trigger.** No current call site does this — every production AddFile call site builds via index.New()/NewBuildTarget on a fresh index, and every Restore/RestoreLazy call site (rankcorpus.go, symbolcorpus.go, diskstore.LoadMmap) never calls AddFile afterward. The trigger is hypothetical: any future code path that calls Index.AddFile on an index obtained from RestoreLazy (e.g. an incremental-refresh feature that reuses an already-loaded mmap shard instead of rebuilding from scratch) or from Restore(blobs, nil) would hit this. This is the one unverified link — flagging as 'likely' rather than 'confirmed' because it requires a caller that does not exist today.

**Impact.** Two distinct failure modes depending on construction path: (1) Restore(blobs, nil) -> ix.postings is nil, so the append-and-assign in addFile panics with 'assignment to entry in nil map' the first time AddFile is called with new content. (2) RestoreLazy(blobs, pp) -> AddFile succeeds (blob + bySHA state update correctly), but the new blob's postings land only in the local ix.postings map, which Postings()/PostingCount()/Trigrams() never read while ix.pp is set. For a non-selective (all-trigram) mmap shard, IndexedGram is unconditionally true, so the query layer trusts the stale provider's empty result as 'zero occurrences' — a silent under-approximation that drops a real match, which is the specific invariant CLAUDE.md calls out as never allowed to happen. (For a selective shard the same gap degrades gracefully to force-scan instead, because IndexedGram there defers to the stale keep-set and reports the new gram as not-indexed.)

**Fix.** Either document AddFile as unsupported on an Index whose pp is set or whose postings map is nil (and have it panic/error clearly up front instead of silently misbehaving), or make addFile route new postings through the same path Postings()/PostingCount() read: e.g. reject AddFile with a clear error when ix.pp != nil, and have Restore initialize ix.postings to an empty map instead of aliasing a caller-supplied nil (matching the invariant New() already establishes) so a future AddFile call fails safe rather than panicking on a nil map write.

#### F-17 — internal/corpus is already a transitive dependency of every default-build production binary

_`internal/ingest/source.go:10` · lane: architecture · confidence: confirmed_

**Status.** Fixed — commit `2665fe6` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** internal/ingest/source.go imports moedex/internal/corpus directly (to call IsManagedRoot/LoadCatalog/LoadLock/DefaultHost). internal/ingest carries no build tag and is imported by cmd/moedex, cmd/moedex-mcp, cmd/moedex-index, cmd/scale, internal/blobstore, and internal/parity (which internal/server/rankcorpus.go also imports untagged). `go list -deps` confirms internal/corpus is a transitive dependency of the default builds of moedex, moedex-mcp, moedex-index, scale, moedex-serve, and moedex-parity -- only cmd/moedex-corpus (the intended importer) and cmd/moedex-nav come out clean. This directly contradicts the explicit invariant stated in CLAUDE.md, ARCHITECTURE.md ('never imported by internal/* or the daemon'), and internal/corpus/runner.go's own package doc.

**Trigger.** Already true today at HEAD -- no special trigger needed; this is a static fact about the current dependency graph, verifiable with `go list -deps ./cmd/moedex-serve/...` and grep.

**Impact.** A plain grep for the import alone would miss this (it only surfaces internal/ingest/source.go plus test files) -- the full blast radius only appears once you follow that one import and re-check each binary's dependency graph. The concrete forward risk: any future dependency added inside internal/corpus (e.g. swapping glab shell-outs for an API client) would silently ship into the network-facing moedex-serve/moedex-mcp daemons with no compiler or CI signal, since the assumed isolation doesn't actually exist.

**Verification.** Confirmed the import edge is real via `go list -deps` across all six default-build binaries, directly contradicting the literal wording of the documented invariant. Downgraded from high to medium because only pure, side-effect-free helpers (IsManagedRoot/LoadCatalog/LoadLock/DefaultHost) cross the boundary -- no risky init-time behavior exists, and the exec-heavy git/glab logic is never actually invoked from any daemon runtime path.

**Fix.** Extract the pure catalog/lock-loading code (IsManagedRoot/LoadCatalog/LoadLock/DefaultHost) that internal/ingest actually needs into a small leaf package with no os/exec/Runner dependency, and have both internal/corpus and internal/ingest depend on that leaf instead of ingest depending on corpus directly.

#### F-18 — Per-file document state grows without bound for the life of a pooled language server

_`internal/navigate/lsp.go:719` · also at `internal/navigate/lsp.go:128,175-191,632,952-972` · lane: resilience · confidence: confirmed_

**Status.** Fixed — commit `3d1132b` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** docFor() never removes entries from c.docs, and textDocument/didClose is advertised in capabilities but never actually sent to the server.

**Trigger.** Completely normal long-lived daemon operation -- any root queried at least once per 10-minute IdleTTL window, which prevents the pool from ever evicting and restarting that server.

**Impact.** Every distinct file ever navigated to pins a full-text copy in memory forever, in both this process and the external language-server process -- unbounded memory growth over the daemon's lifetime with no cap.

**Verification.** Confirmed the docFor/didClose gaps are real and reachable in the production -tags lsp build. Downgraded from high to medium: growth is hard-capped by one repo's distinct file count (not corpus-wide or truly unbounded), and the 10-minute IdleTTL eviction destroys and recreates the whole per-server document map whenever a root goes idle, so the 'forever' framing only holds for a continuously, indefinitely active single root.

**Fix.** Send textDocument/didClose (which the server already advertises support for) and remove the corresponding c.docs entry once a file hasn't been touched for some bounded period, or cap c.docs with an LRU eviction policy.

#### F-30 — A per-query ripgrep invocation failure is adjudicated as a 'justified' RE2-vs-Rust engine quirk instead of a tool error

_`internal/parity/compare.go:132` · also at `internal/parity/run.go:239-248 — rg.run() error path leaves rgSets[i] as its nil zero value instead of a distinguishable value`, `internal/parity/run.go:262 — adjudicate() is called with the run-wide rgAvail flag for every query, not a per-query one`, `internal/parity/report.go:192-198,273-300 — writeQuirks renders the misclassified query under 'Justified engine quirks... not a retrieval error'`, `internal/parity/run.go:331-335 — runZoekt() also treats the same nil rgSets[i] as 'ripgrep found zero matches' truth for the Zoekt differential` · lane: testing · confidence: confirmed_

**Status.** Fixed — commit `980936d` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** adjudicate() decides OK vs VEngineQuirk using only the run-wide `rgAvail` bool (true iff the rg binary was found at all). It has no signal that one specific query's own rg.run() call failed: run.go's rg phase leaves rgSets[i] at its nil zero value on error and records the failure only in a separate rgErrs slice, never threading that per-query failure into adjudicate's rgAvail parameter. compare.go's own comment on the rgAvail==false branch (lines 140-147) explicitly flags this exact risk ('If rg availability ever became per-query, this verdict would need its own gate rather than relying on the run-wide guard') but the per-query case is not actually gated.

**Trigger.** One of the parallel rg.run() calls in run.go (RGParallel workers, one query each) exhausts its 3 retries (oracle_ripgrep.go's newRipgrep/run) and returns an error while rg itself is available and every other query succeeds — a plausible transient fork/exec or resource hiccup when running thousands of rg subprocesses in parallel. That query's slot in rgSets stays nil while rgAvail (the run-wide flag) is still true.

**Impact.** For that query, if moedex correctly matches gold (a real, correct search) with a non-empty result, adjudicate computes MoeMinusRG = minus(moe, nil) = moe (non-empty) and sets Verdict = VEngineQuirk. The query is filed under the report's 'Justified engine quirks (RE2 vs Rust regex)' section with text asserting the divergence is 'not a retrieval error' — which is false; ripgrep never actually produced a result for it. The run's overall HardPass() gate still correctly fails (RGErrors is non-empty), so this does not flip a FAIL to a PASS, but it corrupts per-query triage: an engineer reading the FAIL report sees this query mislabeled as routine, documented noise rather than the tool-crash it is, and the same nil rgSets[i] also feeds runZoekt's covers() check, making moedexCovers/zoektCovers for that query vacuously agree regardless of actual coverage.

**Fix.** Track the set of query IDs whose rg.run() call errored (e.g. alongside rgErrs, a map[int]bool or a per-query bool slice) and pass rgAvail=false into adjudicate specifically for those IDs (or skip EngineQuirk classification entirely for IDs present in rgErrs), matching the guard compare.go's own comment already anticipates for run-wide unavailability.

#### F-19 — parity.Rebuild silently and permanently drops a repo on any non-privacy ingest error

_`internal/parity/manifest.go:473` · lane: testing · confidence: confirmed_

**Status.** Fixed — commit `827ce88` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Rebuild() (used by `moedex-index refresh` in production) treats any non-privacy error from ingest.Repo identically to 'repo genuinely removed from disk' -- silently dropping the repo's content and manifest entry with no field, no log call, and no way for an operator to distinguish a transient git/I/O failure from an actual removal. This is asymmetric with Build()'s equivalent path, which records the same failure class visibly in b.Skipped.

**Trigger.** A transient git or filesystem error (I/O failure, a repo temporarily unreadable during a concurrent operation) during `moedex-index refresh`, for a repo that still legitimately exists on disk.

**Impact.** The repo silently disappears from the index and the manifest with no operator-visible signal, indistinguishable from an intentional removal -- the next refresh won't even notice anything is wrong since the manifest now agrees the repo is gone.

**Verification.** Confirmed the silent-drop defect is real (traced Build()'s correct b.Skipped handling as the contrast). Downgraded from high to medium: the only caller is `moedex-index refresh`, which ARCHITECTURE.md explicitly documents as the 'legacy' shard-level path -- the actual production freshness automation (moedex-corpus sync -reindex -> cas-refresh/cas-export) never calls parity.Rebuild and already has the correct fix (FailedRepos + visible warning) for the identical failure class.

**Fix.** Mirror Build()'s behavior: record this failure class in a visible Skipped-equivalent field on Rebuild's result rather than silently treating it as removal.

#### F-31 — Rank()/Features() dereference a dense-arm blob ID against r.ix without the nil/bounds check the symbol and path arms structurally guarantee

_`internal/rank/ranker.go:390` · also at `internal/rank/ranker.go:410-411 — same unchecked r.ix.Blob(...) feeding lexicalSpans`, `internal/rank/ranker.go:430 — Features() chains r.ix.Blob(c.blob).Files with no nil check`, `internal/rank/ranker.go:764 — lexicalSpans dereferences b.Content assuming b != nil`, `internal/contextwin/contextwin.go:160-167 — the downstream consumer DOES defend against exactly this ("index.Blob panics on an out-of-range ID, so bounds-check first"), but Rank() would already have panicked before contextwin ever receives the RankedResult` · lane: correctness · confidence: likely_

**Status.** Fixed — commit `71d8705` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** Candidate blob IDs from the lexical, symbol, and path arms are all structurally bounded by r.ix.NumBlobs(): buildPathIndex/buildSymbolIndex build their postings by iterating `for id := 0; id < r.ix.NumBlobs(); id++`, and candidateBlobs/tokenCandidateBlobs pull from r.ix/r.ti. The dense arm is the exception — denseArm's blob IDs come directly from `r.store.Search(...)`'s Chunk.Blob field, an external structure not derived from r.ix at query time. fuse(), Rank(), and Features() never validate a candidate's blob ID against r.ix.NumBlobs() before calling r.ix.Blob(id) and dereferencing the result (`b.Files`, `b.Content`), even though index.Index.Blob (internal/index/index.go:215-220) explicitly returns nil for an out-of-range ID rather than panicking itself — the panic happens at the caller's subsequent field access. contextwin.go's own comment ("index.Blob panics on an out-of-range ID, so bounds-check first") shows the codebase is aware of this exact hazard and defends against it one layer downstream, but Rank()/Features() build the RankedResult/BlobFeatures value (dereferencing the field) before that defense is ever reached.

**Trigger.** Ranker.SetDense (or rank.New) installed with an embed.Store whose Chunk.Blob values are not all < r.ix.NumBlobs() — e.g. a store built against a different/larger index than the one passed to New/SetDense. Neither SetDense's contract nor Rank/Features validates this. Today's two production callers (cmd/moedex-mcp/main.go, internal/server/rankcorpus.go's OpenRank) always build the store from the exact same ix passed to New, so the mismatch is not currently exercised — this is the unverified link. A corrupted on-disk embedding sidecar (MDXE) with a garbled per-chunk Blob field would also reach this path, since OpenRank's .meta staleness check (internal/server/rankcorpus.go:369-389) validates aggregate fingerprint/model/NumBlobs/geometry, not individual chunk Blob values.

**Impact.** A nil-pointer-dereference panic inside Ranker.Rank/Features for that one query. Both production entry points wrap query handling in a recover() (internal/mcp/mcp.go:381-399's handleSafe, cmd/moedex-serve/middleware.go's withRecover), so the daemon process itself does not crash, but the specific query fails with an internal error instead of returning results or an empty set, and the failure recurs on every subsequent query that surfaces the same bad blob until the dense arm/store is reset.

**Fix.** Mirror the defensive pattern already used in buildPathIndex/buildSymbolIndex: after computing a candidate's blob in fuse() (or when consuming dense hits in denseArm), check r.ix.Blob(id) != nil and drop the candidate if not, the same way contextwin.Assemble already guards `res.Blob >= uint64(ix.NumBlobs())`. This bounds the dense arm the same way the other three arms are already bounded by construction.

#### F-32 — C# I-prefix heuristic misclassifies any locally-defined class matching I[A-Z]* as an interface, emitting a wrong edge type to the trace_hierarchy MCP tool

_`internal/server/graphhierarchy.go:388` · also at `internal/server/graphhierarchy.go:181 — csExtractInheritance calls csInferEdgeType per super with no positional or resolved-kind cross-check` · lane: correctness · confidence: confirmed_

**Status.** Fixed — commit `151c12c` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** csInferEdgeType classifies every C# base-list entry purely by whether its name starts with 'I' followed by another capital letter, with no use of base-list position (only slot 0 can ever be a base class) and no cross-check against the resolved definition. symbol.Kind (internal/symbol/symbol.go) has only one bucket, `Type`, for 'struct, interface, alias, ...', so nothing else in the pipeline records whether a given locally-defined type is actually declared with the `class`/`struct`/`record` keyword vs `interface` — the name-prefix guess is the only signal used, and it is never corrected even when the super resolves to a local symbol whose actual declaration text is available.

**Trigger.** A C# type in the indexed corpus that derives from, or lists as an interface, a locally-defined class whose name happens to match `I[A-Z]...` (e.g. a locally-defined `IDGenerator`, `IOHelper`, `IPRange`, or `IdentityBase` class used as a base class, matching the same shape as real BCL classes like `System.Net.IPAddress`/`System.IO.IOException` that are classes, not interfaces) — or, conversely, a locally-defined interface that does not follow the I-prefix convention.

**Impact.** resolveAndEmitSuper (graphhierarchy.go:313-385) stores the heuristic's edgeType directly as diskgraph.EdgeExtends or EdgeImplements, which diskgraph.EdgeType.String() renders as the literal `"extends"`/`"implements"` string in GraphEdge.Type (internal/server/graphtools.go:94), returned verbatim by the trace_hierarchy MCP tool. An agent asking the graph 'what does class Foo implement' gets a factually wrong answer (told it implements a class it actually extends, or vice versa) for any such name. The error is confined to the edge's Type label — source/target/confidence are unaffected, and trace_hierarchy's traverse() treats EdgeExtends and EdgeImplements identically for reachability (graphtools.go:628-630), so no edges are lost or duplicated and no traversal breaks.

**Fix.** When sup.superName resolves to a local definition via sweep.merged.Definitions (graphhierarchy.go:323), peek at the target blob's content immediately before def.Start for the literal declaring keyword (`interface` vs `class`/`struct`/`record`) — the same kind of backward keyword scan extract_cs.go already performs when classifying a type's own declaration — and let that override the name heuristic; fall back to the I-prefix guess only when the super is unresolved (external/BCL) content.

#### F-33 — computeEdgesParallel has no panic recovery in its worker goroutines, so any panic anywhere in the candidate/verify/symbol call graph crashes the whole moedex-index build/refresh process with no indication of which name triggered it

_`internal/server/graphrefresh.go:268` · also at `internal/server/graphrefresh.go:219-227 — rebuildAll (full corpus build) drives the same unrecovered worker pool`, `internal/mcp/mcp.go:386 — contrast: the MCP request dispatcher recovers panics per call`, `cmd/moedex-serve/middleware.go:70 — contrast: the HTTP layer recovers panics per request` · lane: resilience · confidence: likely_

**Problem.** The worker goroutine in computeEdgesParallel calls s.computeEdgesForName(names[i], generation) with no recover(). computeEdgesForName transitively calls candidates.GenerateCandidates, graphverify.Verify (regex/pattern classification), and symbol.Corpus.Enclosing across every language-specific extractor in the corpus; none of that call graph is guarded by a recover anywhere in the codebase. A panic in any one of the (now up to GOMAXPROCS concurrently in-flight) names is unrecovered, so the Go runtime terminates the entire process immediately, without running the deferred cleanup registered in RefreshGraphSidecar's own goroutine (sweep.Close(), previous.Close()) and without ever reaching saveGraphSidecar.

**Trigger.** Any panic (nil deref, index-out-of-range, etc.) triggered by processing one of the (potentially tens of thousands of) exported names in a large, heterogeneous corpus, while computeEdgesParallel's worker pool is fanning that name's computation out across GOMAXPROCS goroutines during a full or incremental graph rebuild.

**Impact.** The whole `moedex-index build`/`refresh`/`cas-refresh` process dies with a bare Go panic stack trace that does not print which name was being processed (the loop variable's value never appears in the trace), losing all in-flight work for every other concurrently-running name as well. In the `refresh` subcommand this call happens AFTER the live shard-dir swap has already been committed (main.go:472-478), so the crash leaves the pre-refresh backup directory undeleted and the graph sidecar on the newly-live shard set stuck at its previous generation until the operator re-runs the command (the incremental delta mechanism does correctly pick up and finish the recompute on the next successful run — this is a lost/wasted run and an operational surprise, not permanent corruption).

**Fix.** Wrap the per-name call in each worker with a recover() that converts a panic into an error carrying the offending name (e.g. `defer func(){ if r := recover(); r != nil { errs[i] = fmt.Errorf("panic computing edges for %q: %v", names[i], r) } }()`), consistent with the recover-per-unit-of-work pattern already used at the MCP and HTTP layers (internal/mcp/mcp.go:386, cmd/moedex-serve/middleware.go:70).

#### F-34 — computeEdgesParallel does not fail fast: once one name's computation returns an error, every other already-dispatched and still-queued name keeps running to completion before the error is even inspected, discarding all of that work

_`internal/server/graphrefresh.go:260` · also at `internal/server/graphrefresh.go:278-283 — errs is only scanned after wg.Wait() returns, i.e. after every name has finished` · lane: resilience · confidence: confirmed_

**Problem.** The worker loop `for i := range work { results[i], errs[i] = s.computeEdgesForName(names[i], generation) }` never checks whether any other worker has already recorded an error; the shared `work` channel is drained to completion by every worker regardless. computeEdgesParallel only inspects `errs` after `wg.Wait()`, and on finding any non-nil error it discards `results` entirely (`return nil, err`).

**Trigger.** computeEdgesForName legitimately returns a non-nil error for one name among many thousands eligible in a full or incremental rebuild (e.g. one of the explicit internal-consistency guards in graphsidecar.go's computeEdgesForName — 'references an unknown blob', 'has a negative byte offset' — fires for a single name due to a data inconsistency).

**Impact.** On a corpus large enough that a full/incremental graph rebuild takes minutes to hours, hitting one such error wastes the full remaining wall-clock time of the run (all workers keep pulling and computing names from the channel until it's drained) before the run is aborted and every bit of that computed work is thrown away with no partial credit — worse than failing immediately, since the operator has to wait out the entire doomed run to find out it failed.

**Fix.** Add an early-exit path once any error is observed — e.g. a shared atomic flag or a small buffered error channel checked at the top of the worker's per-item loop, or switch to golang.org/x/sync/errgroup-style cancellation via a context that the loop selects on alongside the work channel — so a data problem discovered early stops burning the rest of the run's compute budget.

#### F-25 — graphtools.go reaches into SymbolCorpus's private fields instead of its documented exported surface, one of the two reaches with no public substitute at all

_`internal/server/graphtools.go:161` · also at `internal/server/graphtools.go:168 — s.symbols.corpus.Symbols(shard, id), a private-field call that could already be s.symbols.Merged().Symbols(shard, id)`, `internal/server/symbolcorpus.go:59-63 — SymbolCorpus's unexported idxs/corpus fields`, `internal/server/symbolcorpus.go:153 — Merged(), the documented accessor, correctly used instead by symbolcorpus_test.go:137-151 but bypassed by graphtools.go` · lane: architecture · confidence: confirmed_

**Status.** Fixed — commit `81b3b85` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** graphSnapshot.buildCatalog (graphtools.go:159-203) iterates `s.symbols.idxs` and calls `s.symbols.corpus.Symbols(...)` directly on SymbolCorpus's unexported fields, rather than through SymbolCorpus's documented public surface (Merged(), NumShards(), References(), Definitions(), Locate()). SymbolCorpus's own header comment (symbolcorpus.go:1-23) describes its job as 'builds one symbol index per shard and merges them into a single cross-shard byName lookup... resolves shard-qualified refs back to repo/path/line' — it does not mention being used as a generic per-shard-blob enumerator for a second consumer's catalog-building pass. One of the two reaches (`.corpus.Symbols`) has a trivial fix via the existing Merged() accessor (used correctly in symbolcorpus_test.go); the other (`.idxs`, needed to enumerate every blob in every shard) has no public equivalent at all, because SymbolCorpus's real API only supports name-keyed lookups, not raw shard enumeration.

**Trigger.** Any future change to SymbolCorpus's internal representation made by someone relying only on its documented contract (e.g. dropping per-shard *index.Index retention after the merge, which its own doc comment invites by describing shard-level independence) — a reasonable refactor motivated purely by SymbolCorpus's stated job.

**Impact.** Because the reach is same-package (not a compiler-enforced boundary crossing package lines), such a refactor breaks graphtools.go's buildCatalog at compile time with no import-graph signal pointing at the coupling; a maintainer has to already know graphtools.go depends on SymbolCorpus's storage shape, not just its documented methods. It also means SymbolCorpus's public API and doc comment currently understate what the type is actually used for in this package.

**Fix.** Replace `s.symbols.corpus.Symbols(shard, id)` with `s.symbols.Merged().Symbols(shard, id)` (zero-cost, uses the existing accessor). Add a small exported accessor on SymbolCorpus for per-shard blob enumeration (e.g. `func (sc *SymbolCorpus) NumShardBlobs(shard int) int` / `func (sc *SymbolCorpus) ShardBlob(shard int, id uint64) *index.Index` or similar), and have graphtools.go's buildCatalog go through it instead of `s.symbols.idxs` directly, so SymbolCorpus's real contract is visible in its own file.

#### F-35 — An HTTP_CALLS handler with no enclosing named symbol is unreachable by file-based graph-tool lookups

_`internal/server/httpgraph.go:28` · also at `internal/graph/httproute/extract.go:211,217-223 — SymbolStart is left at -1 when no owner is passed and Enclosing() finds nothing`, `internal/server/graphtools.go:193-198 — buildCatalog only catalogs diskgraph.Keys() (edge sources), never target-only keys`, `internal/server/graphtools.go:815-827 — rootsForFile can only return nodes already in s.nodes` · lane: correctness · confidence: likely_

**Status.** Fixed — commit `81b3b85` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** addHTTPCallEdges keys the HTTP_CALLS edge's target at edge.Handler.SymbolStart, falling back to the raw literal's own byte offset (edge.Handler.Start) via httpNodeOffset when the handler has no enclosing symbol (SymbolStart == -1, per extract.go). When that fallback fires, the target node is a raw-evidence key that is never a diskgraph.Keys() source (nothing calls the route-registration literal itself) and never a symbol.BuildMulti entry, so it never lands in graphSnapshot.nodes.

**Trigger.** A route registration with no enclosing named function/method — e.g. a top-level Express `app.get('/x', ...)` call in module scope, or a C# minimal-API `app.MapGet(...)` lambda outside any method — so extract.go's emitOwned leaves owner nil and Enclosing() finds nothing, producing SymbolStart=-1 for the handler side. This path is exercised by the fallback code in httpNodeOffset but not by any test (httpgraph_test.go's fixture uses a named Go handler function, so TargetOffset there is a real symbol NameStart and resolves fine).

**Impact.** impact_analysis(file=<file containing the anonymous handler>) cannot find that handler as a root via rootsForFile, so it silently omits the cross-service HTTP callers of that route from the blast-radius result — the same class of missing-result failure as the manifest finding above, but reachable only for handlers lacking an enclosing symbol rather than universally.

**Fix.** Same remedy as the manifest finding: have graphtools.go's buildCatalog also register nodes that appear only as edge targets (e.g. by folding graph.EachEdge's edge.Target keys into s.nodes when absent), so a raw-evidence HTTP handler node becomes locatable by rootsForFile the same way a named-symbol handler already is.

#### F-20 — Manifest DEPENDS_ON edges key their source and target nodes inconsistently (and a second, distinct bug: leaf-dependency manifests never become graph nodes at all)

_`internal/server/manifestsidecar.go:106` · also at `internal/server/manifestsidecar.go:129` · lane: correctness · confidence: confirmed_

**Status.** Fixed — commit `40e5892` on `codebase-review-fixes/2026-08-18` · tests added: yes.

**Problem.** A DEPENDS_ON edge's target is keyed at the raw, non-zero identity-declaration offset (resolved.Target.Offset) while the same function forces the source side to the canonical whole-file key manifestNodeOffset (0). The same manifest file thus gets two different graph node identities depending on its role, and graphtools.go's traversal does exact-key matching. Verification additionally found the finding's own cited test fixture (a pure-leaf target manifest with no outgoing dependencies) fails for a SEPARATE, unaddressed reason: addManifestEdges never calls builder.AddNode for a pure dependency target, so that blob never becomes a graph node at any offset regardless of the keying fix.

**Trigger.** Only reachable through BuildGraphWithOptions, not through the default moedex-index build/refresh path (RefreshGraph's rebuildAll skips addManifestEdges entirely per C1) -- so today it fires only via an onnx-tagged/make build-dense graph build, via a direct BuildGraph/BuildGraphWithOptions test or tool call, or via the undocumented unconditional cmd/rebuild-graph binary. Within that path it manifests concretely when a manifest is BOTH a DEPENDS_ON source (giving it a node at manifestNodeOffset via its own declared dependency) AND the target of another manifest's dependency (e.g. a shared 'core'/'common' library depended on by other repos while itself depending on something else): impact_analysis(file=<that manifest>) finds the node but reports zero dependents. For a pure-leaf target manifest with no outgoing dependencies of its own (the finding's cited writeManifestShards repro), impact_analysis also returns empty, but that is a separate, unaddressed defect -- addManifestEdges never calls builder.AddNode for the edge's target, so the target blob never becomes a node at any offset, and forcing TargetOffset to manifestNodeOffset (the finding's proposed fix) does not change that, as verified by direct test.

**Impact.** impact_analysis(file=<a manifest with real dependents>) silently returns an empty result instead of the true dependents -- this is the flagship intended use case for the manifest-based graph phase, and it comes back empty with no error. (Note: this bug is currently masked in the default build by finding C1, since manifest edges aren't produced there at all today -- it becomes live the moment C1 is fixed or on any onnx-tagged full build.)

**Verification.** Built a 3-manifest 'hub' scenario and empirically confirmed the keying asymmetry causes impact_analysis to miss real dependents. Separately proved, by hand-building a graph with the finding's proposed fix applied, that it does NOT resolve the finding's own cited repro case -- that requires also registering pure-leaf targets as nodes, a distinct gap. Downgraded from high to medium since this whole code path (addManifestEdges) is dormant in the default build today (confirmed via the related graph-edge-loss finding) and live only via the onnx-tagged build or the undocumented cmd/rebuild-graph dev binary.

**Fix.** Key the target the same way the source already is, at manifestNodeOffset (0), so the same manifest file always resolves to one consistent node identity regardless of which side of an edge it's on. Additionally, addManifestEdges must call builder.AddNode for the edge's target even when that manifest has no outgoing dependencies of its own, or leaf dependency targets will never be resolvable via impact_analysis regardless of the keying fix.

### Low

#### F-39 — onnx-tagged buildGraph() bypasses RefreshGraph's incremental machinery by default and discards the real GraphRefreshStats, so operators see stats that don't reflect the (unconditional full) rebuild that just ran

_`cmd/moedex-index/graph_onnx.go:37` · also at `cmd/moedex-index/graph_onnx.go:15 — buildGraph's named return `stats server.GraphRefreshStats` is left at its zero value on this branch`, `internal/server/graphbuild.go:41-46 — DefaultSimilarTopK = 5, so topK is non-zero unless MOEDEX_GRAPH_SIMILAR_TOP_K=0 is explicitly set` · lane: graph-refresh-consistency · corroborated by: chunk-02__graph-refresh-consistency (rated high there; downgraded here since re-analysis showed the behavior is safe, just non-obvious to operators) · confidence: confirmed_

**Problem.** Under -tags onnx, buildGraph() takes `topK, err := graphIntEnv("MOEDEX_GRAPH_SIMILAR_TOP_K", server.DefaultSimilarTopK)`; since DefaultSimilarTopK is 5, topK is non-zero by default, so every call -- whether the operator ran `moedex-index build` or `moedex-index refresh` -- goes through `server.BuildGraphWithOptions` (line 37), an unconditional full O(corpus) rebuild, and never through server.RefreshGraph's incremental path at all. This does avoid the missing-passes bug above (BuildGraphWithOptions always runs every pass), but it means phase-12 incremental refresh is effectively dead code for onnx builds under default settings, and the `stats` GraphRefreshStats returned to the caller is discarded (`path, _, err = server.BuildGraphWithOptions(...)`), so the named return `stats` stays at its zero value.

**Trigger.** Build moedex-index with -tags onnx and leave MOEDEX_GRAPH_SIMILAR_TOP_K unset (the default); run `moedex-index refresh` on a shard dir that already has a graph.

**Impact.** printGraphStats(stats) in cmd/moedex-index/main.go reports FullRebuild=false, zero blobs/names/edges recomputed, etc., even though a full rebuild of every name in the corpus just happened -- misleading operator-facing freshness/cost telemetry. Functionally the graph content itself stays correct (all edge types present), so this is an observability gap, not a data-correctness gap.

**Fix.** Either have the onnx-tagged buildGraph() populate a GraphRefreshStats from the GraphBuildReport it gets back from BuildGraphWithOptions (at least FullRebuild=true and node/edge counts), or route the onnx similarity pass through RefreshGraph's own incremental machinery (once the primary finding above is fixed to run the tag-independent passes there) so onnx builds actually get incremental refresh instead of an unconditional full rebuild every time.

#### F-37 — ftoa() emits a non-digit character for MaxDocFraction in [0.995, 1.0)

_`internal/index/selector.go:81` · lane: correctness · confidence: confirmed_

**Problem.** ftoa's rounding step `frac := int((f-float64(whole))*100 + 0.5)` can evaluate to 100 for f in [0.995, 1.0) (whole=0), and the subsequent `d1 := byte('0' + frac/10)` then computes byte('0'+10), which is ':' (ASCII 58), not a digit.

**Trigger.** Running `moedex-index build -selective -gram-max-df 0.995` (or any value in [0.995, 0.999]) or setting `MOEDEX_SELECTIVE=0.995` for cmd/scale, then observing the printed 'selective build'/'selective index enabled' log line, which calls FrequencyThresholdSelector.Describe() -> ftoa(0.995). Verified directly: ftoa(0.995) through ftoa(0.999) all return the string "0.:0" instead of a value like "0.99"/"1.00".

**Impact.** The selective-index enablement log line prints a garbled fraction (e.g. 'freq-threshold(max-df-fraction=0.:0)') instead of the operator's configured threshold. Cosmetic only — it does not affect which grams are selected (Select() uses the raw float, not ftoa's output), so search correctness and ripgrep parity are unaffected.

**Fix.** Round-then-carry: if frac == 100, increment whole and reset frac to 0 before rendering (mirroring how strconv/fmt handle the carry), or simply replace ftoa with fmt.Sprintf("%.2f", f) since this is only used for a diagnostic label and the 'avoid fmt' comment's benefit is negligible here.

#### F-40 — MOEDEX_VERIFY_CONTENT false-y matching is case-inconsistent, so some spellings silently fail to disable verification

_`internal/server/dedup.go:67` · lane: correctness · confidence: confirmed_

**Problem.** contentHasher() switches on the literal env value against a hand-picked list — "0", "false", "no", "off", "FALSE", "No", "Off" — that mixes cases inconsistently (both "false" and "FALSE" are covered, but "NO"/"OFF" all-caps and "True"-style mixed variants are not), rather than normalizing case once (e.g. strings.ToLower) before comparing.

**Trigger.** An operator sets MOEDEX_VERIFY_CONTENT=NO (or OFF, or any case spelling not in the exact list) intending to skip the one-time content-store integrity hash on a very large corpus.

**Impact.** The unmatched value falls through to the default branch, which keeps verification on. This fails safe (no correctness/data-integrity harm), but it silently ignores the operator's documented opt-out, so a large-corpus boot pays the full verification cost the operator explicitly tried to avoid, with no error or warning that the flag didn't take effect.

**Fix.** Normalize with strings.ToLower(os.Getenv("MOEDEX_VERIFY_CONTENT")) before the switch, or switch over a small canonical set ("0", "false", "no", "off") post-lowercasing, so every case spelling of the same word behaves identically.

#### F-38 — C# `global::`/alias-qualifier syntax in a base list produces a spurious unresolvable 'global' pseudo-supertype

_`internal/server/graphhierarchy.go:508` · lane: correctness · confidence: confirmed_

**Problem.** hierCollectIdents' dotted-name skip (lines 508-534) only recognizes a single '.' separator between identifier segments, not the C# `::` alias-qualifier token (used by `global::` and `extern alias` references). For input like `class Foo : global::System.IDisposable`, the scanner reads 'global' as a complete identifier, then hits ':' which is neither '.' nor a comma nor `{`/`;`, so the outer loop's `if i == start { i++; continue }` fallback skips the two ':' characters one byte at a time before resuming on 'System.IDisposable'.

**Trigger.** Any C# base-list entry written with an alias qualifier, most commonly `global::SomeType` (used to disambiguate namespace conflicts, and common in some source-generated or defensively-qualified code).

**Impact.** Two inheritedSuper entries are produced instead of one: a bogus `{name: "global", edgeType: Extends}` (since 'global' does not match the I-prefix pattern) and the correctly-parsed real entry (e.g. `{name: "IDisposable", edgeType: Implements}`). The real relationship is still captured, so no true edge is lost; the bogus 'global' entry fails to resolve in resolveAndEmitSuper (sweep.merged.Definitions("global") returns nothing) and only inflates HierarchyReport.UnresolvedSupers, which is a stats-only field (internal/server/graphbuild.go:198-208) not used for any pass/fail decision. No infinite loop or crash: the byte-by-byte fallback always advances forward.

**Fix.** Extend the dotted-name skip in hierCollectIdents (and its TS-side mirrors, if extern alias syntax is ever a concern) to also match `::` as a segment separator, e.g. treat `content[j]==':' && j+1<n && content[j+1]==':'` the same as `content[j]=='.'` before falling through to identifier collection.

#### F-36 — impact_analysis's file-path resolution does a full linear scan of every graph node instead of using the byPath index already built for exactly this lookup

_`internal/server/graphtools.go:815` · also at `internal/server/graphtools.go:598-608 — impactAnalysis calling rootsForFile for the file-keyed case`, `internal/server/graphtools.go:205-230 — buildPathIndex/s.byPath, the indexed structure that solves 'file -> nodes at line' but isn't consulted here`, `internal/server/graphneighbors.go:328-350 — anchorsFor, the sibling consumer that does use s.byPath for the same class of question` · lane: architecture · confidence: confirmed_

**Problem.** rootsForFile iterates `for key, meta := range s.nodes` — every graph node in the whole corpus catalog — calling graphPathMatches against every one of that node's locations, even though graphSnapshot already builds and maintains s.byPath (buildPathIndex, graphtools.go:205-230) specifically to answer 'which nodes live in this file' in O(1). The suffix-matching semantics impact_analysis's schema advertises ('Absolute, repo-relative, or suffix file path') are looser than byPath's exact-spelling keys, so a direct swap isn't free, but the existing index is never even consulted as a first-pass narrowing step.

**Trigger.** Any impact_analysis call with a `file` argument on a corpus large enough that s.nodes is not trivially small — the documented target deployment for this warm daemon is a full multi-repo corpus (hundreds of repos), where s.nodes holds every extracted symbol occurrence plus every raw graph-evidence node corpus-wide.

**Impact.** Every impact_analysis-by-file call costs O(total corpus graph nodes) regardless of how many nodes actually live in the requested file, unlike every other lookup in this file (trace_calls/trace_consumers key off the indexed bySymbol map, and graphneighbors.go's per-block anchoring keys off byPath) — a latency characteristic that degrades linearly as the corpus grows and that nothing in this codebase's existing structure would surface until someone runs it against a large corpus by hand.

**Fix.** Have rootsForFile probe s.byPath with the candidate spellings first (as anchorsFor does) to get a narrow candidate set for exact/prefix matches, falling back to the linear scan only for genuine bare-suffix queries that don't match any indexed spelling — or extend buildPathIndex to also index by path suffix segments so the common case (repo-relative or absolute path passed verbatim) is O(1).

---

## Lane notes

Observations that did not clear the bar for a finding but are worth knowing.

### architecture

The stated invariant that internal/corpus is 'never imported by the engine or daemon' is already false today (internal/ingest imports it, and that import reaches every default-build binary except moedex-corpus itself) — though verification found the actual crossing is limited to inert helper functions, not the risky git/glab-shelling logic, capping the real-world consequence at medium.

### binary-format-safety

A systemic pattern, not a one-off: three independent reviewers, working on three different hand-rolled binary formats (trigram index, graph adjacency, symbol sidecar) with no knowledge of each other's work, independently found the identical defect class — a header-provided element count used to size an allocation before being validated against the file's actual remaining bytes. One instance was empirically reproduced at ~78-82GB of RSS from a single corrupted count field.

### graph-refresh-consistency

The single largest cluster of findings in this review. The knowledge-graph layer (internal/server/graph*.go, internal/graph/*) is under active, fast-moving construction — three separate commits landed in the working tree during the course of this review, including a brand-new type-hierarchy feature reviewed in a supplementary pass. Every non-symbol-reference edge family (LSP calls, ONNX similarity, HTTP routes, manifest dependencies, type hierarchy) shares the same root cause: the incremental refresh path and the full-build path are not actually kept in sync with each other, despite an internal doc comment promising they are.

### privacy-boundary

Mixed picture. internal/ingest's core fail-closed policy engine and internal/corpus's repo-acquisition layer both held up well under adversarial review, with one real and serious exception: a directory-scoped override silently does nothing if the operator omits a trailing slash. Separately, the experimental LSP navigation arm is confirmed to bypass the privacy policy entirely across all three of its callers, a gap the project's own ADR 0021 already tracks but has not yet closed.
