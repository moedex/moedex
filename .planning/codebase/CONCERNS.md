# Codebase Concerns

**Analysis Date:** 2026-08-24

Baseline: `CODEBASE-REVIEW.md` (generated 2026-08-17) enumerated 40 findings; all 11 High and all 24 Medium were remediated and merged (`0dcc3ef`). The 5 Low findings have never had a remediation wave and were re-verified as still present in the working tree during this analysis. Everything below is current-state.

## Tech Debt

**Stated "pure Go standard library" invariant is false in the default build:**
- Issue: `CLAUDE.md:36` and `ARCHITECTURE.md:12` both assert the default build is "pure Go standard library with zero ML/runtime deps" and that only the `onnx`/`moedex_simd` tags may add modules. `internal/mcp/sdkserver.go` (no build tag) imports `github.com/modelcontextprotocol/go-sdk`, which pulls a large untagged transitive closure into `cmd/moedex-serve`: `github.com/google/jsonschema-go`, `golang.org/x/oauth2`, `golang.org/x/sync/errgroup`, `golang.org/x/time/rate`, `github.com/yosida95/uritemplate/v3`, `github.com/segmentio/encoding/json`, and `github.com/segmentio/asm/*` (which ships hand-written assembly and an `internal/unsafebytes` package).
- Files: `internal/mcp/sdkserver.go`, `go.mod`, `CLAUDE.md`, `ARCHITECTURE.md`, `docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md`
- Impact: The single loudest project invariant no longer describes reality, and ADR 0022 — the decision record that introduced the SDK — never mentions the dependency trade-off at all. Any future contributor reading `CLAUDE.md` will either wrongly reject a needed dependency or wrongly assume the audited-surface guarantee still holds.
- Fix approach: Decide which is true and make the docs match. Either amend `CLAUDE.md`/`ARCHITECTURE.md` to say "standard library plus the MCP SDK in the serving path; zero ML/runtime deps" and add a "Consequences → dependencies" section to ADR 0022, or move `sdkserver.go` behind a build tag so the pure-Go claim survives literally.

**`internal/server` is a 10,168-line package that owns the entire graph layer:**
- Issue: 17 of the package's 23 non-test files are `graph*.go`, totalling ~8,300 lines, even though `internal/graph/` exists as a package tree (`candidates`, `cluster`, `diskgraph`, `httproute`, `manifest`, `verify`). Graph construction, refresh, query tools, hierarchy extraction, call audit, discovery, injection, and rendering all live in the serving package rather than the graph package.
- Files: `internal/server/graphtools.go` (1238), `internal/server/graphcalls_lsp.go` (972), `internal/server/graphbuild.go` (932), `internal/server/graphrefresh.go` (820), `internal/server/graphhierarchy.go` (765), `internal/server/graphcallaudit.go` (639), `internal/server/graphdiscovery.go` (623)
- Impact: Everything graph-related is compiled into the daemon whether or not it's used, the dependency direction is one-way (`internal/server` imports `internal/graph`, never the reverse), and there is no compiler-enforced seam between "build the graph" and "serve the graph". This is the largest single obstacle to testing graph construction without standing up a server.
- Fix approach: Move construction (`graphbuild.go`, `graphrefresh.go`, `graphhierarchy.go`, `graphcallaudit.go`) into `internal/graph/build`, leaving `internal/server` with only the MCP/HTTP tool surface. Do this after the graph layer's churn rate drops — see "Fragile Areas".

**Environment-variable configuration sprawl:**
- Issue: 49 distinct `MOEDEX_*` variables are read across `internal/` and `cmd/`; only 24 appear anywhere in `README.md`, `ARCHITECTURE.md`, `deploy/`, or `docs/`. There is no central registry, no validation pass at startup for the majority, and no way for an operator to discover the full set.
- Files: `cmd/moedex-serve/config.go`, `cmd/moedex-index/graph_onnx.go`, `internal/server/dedup.go`, `internal/server/graphbuild.go`, `internal/embed/embed.go`
- Impact: Undocumented knobs (`MOEDEX_MMAP`, `MOEDEX_SELECTIVE`, `MOEDEX_GRAPH_CLUSTER_MAX_NODES`, `MOEDEX_MCP_MAX_CONCURRENCY`, `MOEDEX_SEARCH_MAX_CONCURRENCY`) silently change engine behavior. A typo in a variable name is indistinguishable from leaving it unset.
- Fix approach: Introduce a single `internal/config` registry that declares every variable with its type, default, and doc string; have `cmd/moedex-serve` and `cmd/moedex-index` validate against it at startup and warn on any `MOEDEX_*` in the environment that isn't registered. Generate the operator table in `README.md` from that registry.

**`CODEBASE-REVIEW.md` is a 109 KB tracker checked into the repo root:**
- Issue: The file mixes a point-in-time audit (2026-08-17) with live remediation status, and its "Remediation status" table is now stale — it lists 35/40 fixed but says nothing about the Low wave never having run, and it has not been updated across the eleven commits landed since.
- Files: `CODEBASE-REVIEW.md`
- Impact: A dated audit sitting at the repo root next to `ARCHITECTURE.md` and `README.md` reads as current guidance. Its Low findings (below) are the only remaining tracked work, buried at line 619 of a 701-line file.
- Fix approach: Promote the 5 open Low findings into whatever issue tracker the project uses, add a header line to `CODEBASE-REVIEW.md` marking it as a closed 2026-08-17 audit, and move it under `docs/`.

**`gofmt` drift is unenforced by any gate:**
- Issue: `gofmt -l internal cmd` reports 27 files needing reformatting, totalling 262 changed lines. Nothing enforces it — `make health` is `build vet test`, and neither the `Makefile` nor `.gitlab-ci.yml` nor `.vscode/` mentions `gofmt`, `go fmt`, or `goimports` anywhere.
- Files: 23 test files plus 4 non-test sources — `internal/server/graphhierarchy.go`, `internal/server/graphrenders.go`, `internal/server/httpgraph.go`, `internal/eval/gold_fixture.go`
- Impact: The drift is struct-field and composite-literal key alignment (e.g. `HierarchyReport`'s field block at `internal/server/graphhierarchy.go:20-28`, and the `diskgraph.Edge` literal at `internal/server/httpgraph.go:57-59`), not cosmetic whitespace at line ends — so any future edit that adds a longer field name produces a re-alignment diff touching unrelated lines, muddying blame. Three of the four non-test offenders are in the `internal/server` graph code, the highest-churn area in the repo, which is exactly where clean diffs matter most.
- Fix approach: Run `gofmt -w internal cmd` as a single isolated commit, then add `gofmt -l internal cmd` (failing on non-empty output) to the `health` target so `make health` catches it on every push. This is the cheapest gate in this document to add.

**ONNX graph builds never take the incremental refresh path:**
- Issue: `buildGraph` under `-tags onnx` reads `MOEDEX_GRAPH_SIMILAR_TOP_K` defaulting to `server.DefaultSimilarTopK` (5), so `topK == 0` is false unless explicitly set, and every call — `build` *and* `refresh` — routes to `server.BuildGraphWithOptions`, an unconditional full O(corpus) rebuild. `server.RefreshGraph`'s incremental machinery is reachable only by explicitly setting `MOEDEX_GRAPH_SIMILAR_TOP_K=0`, which also disables the similarity arm.
- Files: `cmd/moedex-index/graph_onnx.go:31-33`, `internal/server/graphbuild.go`
- Impact: Phase-12 incremental graph refresh is effectively dead code for dense builds. The operator-facing stats gap flagged as F-39 has since been fixed (the function now populates `stats.FullRebuild`, `stats.NamesRecomputed`, `stats.EdgesRecomputed` from the build report), so this is now a cost problem rather than an observability one — but every scheduled dense refresh pays a full corpus rebuild.
- Fix approach: Route the ONNX similarity pass through `RefreshGraph`'s incremental scheduler as an additional pass, rather than short-circuiting to the full builder whenever similarity is enabled.

## Known Bugs

All three are open Low findings from the 2026-08-17 review, re-verified present on 2026-08-24. None affect ripgrep parity or search correctness.

**`ftoa` emits a non-digit for fractions in [0.995, 1.0) (F-37):**
- Symptoms: `ftoa(0.995)` through `ftoa(0.999)` return the string `"0.:0"` — `frac` rounds to 100, and `byte('0' + frac/10)` produces `':'` (ASCII 58).
- Files: `internal/index/selector.go:81-85`
- Trigger: `moedex-index build -selective -gram-max-df 0.995`, or `MOEDEX_SELECTIVE=0.995` for `cmd/scale`; the garbled value appears in the "selective index enabled" log line via `FrequencyThresholdSelector.Describe()`.
- Workaround: Use a threshold below 0.995. Cosmetic only — `Select()` uses the raw float, so gram selection and parity are unaffected.

**`MOEDEX_VERIFY_CONTENT` false-y matching is case-inconsistent (F-40):**
- Symptoms: The opt-out silently does nothing for spellings not in the hand-picked list. `contentHasher()` switches on `"0", "false", "no", "off", "FALSE", "No", "Off"` — so `FALSE` works but `NO` and `OFF` do not, and fall through to the verifying default.
- Files: `internal/server/dedup.go:66-72`
- Trigger: Set `MOEDEX_VERIFY_CONTENT=NO` (or `OFF`) intending to skip the one-time content-store integrity hash on a large corpus.
- Workaround: Use exactly `0`, `false`, `no`, or `off`. Fails safe (verification stays on), but the operator pays the full boot-time hash pass with no warning that the flag was ignored.

**C# `global::` alias qualifiers produce a spurious pseudo-supertype (F-38):**
- Symptoms: `class Foo : global::System.IDisposable` yields two `inheritedSuper` entries — a bogus `{name: "global", edgeType: Extends}` plus the correct `IDisposable` entry. `hierCollectIdents`' dotted-name skip recognizes `.` as a segment separator but not `::`, so the byte-by-byte fallback steps over the two colons.
- Files: `internal/server/graphhierarchy.go` (`hierCollectIdents`, dotted-name skip loop)
- Trigger: Any C# base list written with an alias qualifier — common in source-generated or defensively-qualified code.
- Workaround: None needed. The real relationship is still captured; the bogus entry fails to resolve and only inflates `HierarchyReport.UnresolvedSupers`, a stats-only field.

## Security Considerations

**Daemon authentication is opt-in and only warned about:**
- Risk: `moedex-serve` starts and serves `/search`, `/stats`, and `/mcp` with no token when `MOEDEX_AUTH_TOKEN` and `-auth-token` are both unset. On a non-loopback bind this logs `"no auth token AND non-loopback bind; /mcp is open to the network"` and then serves anyway.
- Files: `cmd/moedex-serve/main.go:483-485`, `cmd/moedex-serve/main.go:759-761`, `cmd/moedex-serve/middleware.go:204-215`
- Current mitigation: Genuinely good defense-in-depth — an addr with an empty host binds `127.0.0.1` unconditionally (`resolveAddr`, and the comment explicitly notes this is not token-conditional), token comparison uses `subtle.ConstantTimeCompare` (`middleware.go:174`), and TLS is all-or-nothing so a half-configured cert/key pair is a startup error.
- Recommendations: Make a non-loopback bind without a token a *fatal* startup error rather than a warning, with an explicit `-allow-insecure` escape hatch. The daemon serves the full text of every indexed repository; a warning in a log nobody reads is thin protection for that.

**LSP navigation arm reads the live working tree, outside the index privacy boundary:**
- Risk: ADR 0021 scopes the AI-privacy guarantee to index-backed retrieval only, and says so explicitly: "Index filtering alone does not make an external language server privacy-safe: it may scan an entire workspace or dependencies." Under `-tags lsp`, `mcp.HashFiles` opens navigation-result paths directly from disk (`os.ReadFile`) to compute blob SHAs for cache identity, and the external language server itself reads whatever it wants.
- Files: `internal/mcp/result.go:135-190` (`HashFiles`), `cmd/moedex-serve/nav_lsp.go:148`, `cmd/moedex-serve/nav_lsp.go:218`, `internal/navigate/pool.go:297-380`
- Current mitigation: Solid at the entry point — `NavigatorFor` runs a fail-closed privacy preflight before ever spawning a server for a root and returns `ErrPrivacyRestricted` for any level-1 workspace (`internal/navigate/pool.go:79-88`); the graph LSP sweep preflights every repository policy before the first server starts. The arm is `-tags lsp` only and absent from the default binary.
- Recommendations: Keep the arm out of any deployment serving level-1-adjacent corpora until the "sanitized workspace" work ADR 0021 names is actually built. `HashFiles` should refuse paths outside the privacy-approved set rather than trusting that the preflight already filtered them, so the guarantee doesn't depend on caller discipline.

**Shell-out surface for corpus acquisition:**
- Risk: `internal/corpus` and `internal/ingest` invoke `git` and `glab` with operator- and GitLab-API-derived arguments.
- Files: `internal/corpus/runner.go:90`, `internal/corpus/clone.go:125` (`validRef`), `internal/ingest/ingest.go:58`, `internal/ingest/ingest.go:81`, `internal/version/sourcehash/sourcehash.go:99`
- Current mitigation: Correctly done — every call uses `exec.Command`/`exec.CommandContext` with an argument vector, never a shell string, and `validRef` explicitly rejects refs that look like flags. `internal/corpus/catalog` is a deliberately exec-free leaf so `internal/ingest` can import it without pulling the git/glab machinery into every binary (documented at `internal/corpus/catalog/catalog.go:1-17`); this closes the boundary violation the 2026-08-17 review's architecture lane flagged.
- Recommendations: No change needed. Preserve the exec-free property of `internal/corpus/catalog` — it is load-bearing for the corpus-isolation invariant and nothing currently enforces it in CI.

**Dependency provenance for the dense arm:**
- Risk: `go.mod` carries `replace github.com/sugarme/tokenizer => github.com/clems4ever/tokenizer v0.0.0-20250926133620-9ddc80533c43` — a personal fork pinned to a pseudo-version rather than a tagged release.
- Files: `go.mod:28`
- Current mitigation: Confined to `-tags onnx`; `go.sum` pins the hash; the fork is not in the default build's dependency closure.
- Recommendations: Document why the fork is required (which upstream bug it fixes) in ADR 0007 or a comment beside the `replace`, and set a trigger for dropping it. An undocumented `replace` on a personal fork is the kind of thing that outlives everyone who knows why it exists.

## Performance Bottlenecks

**`impact_analysis` by file scans every graph node in the corpus (F-36):**
- Problem: `rootsForFile` iterates `for key, meta := range s.nodes` — every graph node corpus-wide — calling `graphPathMatches` against each of that node's locations.
- Files: `internal/server/graphtools.go:1205-1218`, called from `internal/server/graphtools.go:879`
- Cause: `graphSnapshot` already builds and maintains `s.byPath` (`internal/server/graphtools.go:58-62`, populated at `:302`) specifically to answer "which nodes live in this file" in O(1), and the sibling consumer `anchorsFor` uses it correctly (`internal/server/graphneighbors.go:398`). `rootsForFile` never consults it, because the tool's advertised suffix-matching semantics are looser than `byPath`'s exact-spelling keys.
- Improvement path: Probe `s.byPath` with the candidate spellings first, as `anchorsFor` does, and fall back to the linear scan only for genuine bare-suffix queries. Alternatively extend `buildPathIndex` to index path suffix segments so the common case (a verbatim absolute or repo-relative path) is O(1).

**Index build and serve both peak near 9 GB RSS:**
- Problem: The 2026-08-21 full-corpus run recorded 8,627.9 MB build peak RSS and 9,171.1 MB run peak RSS for 491 repos / 63,523 files / 969.5 MB of indexed content.
- Files: `PARITY-REPORT.md` (Corpus & build table), `internal/diskstore/diskstore.go`, `internal/blobstore/`
- Cause: Postings stay off the Go heap via mmap and the deduped `MOEDEX05` format stores each blob once corpus-wide, so this is already the optimized path — the remaining resident set is index construction state and sidecar building.
- Improvement path: This is a documented characteristic more than a defect, but it sets the single-node ceiling (see Scaling Limits). Anyone doubling corpus size should measure before assuming headroom.

**ONNX graph refresh is always a full rebuild:** see "Tech Debt → ONNX graph builds never take the incremental refresh path".

## Fragile Areas

**Graph layer, under active high-velocity construction:**
- Files: `internal/server/graph*.go`, `internal/graph/{candidates,cluster,diskgraph,httproute,manifest,verify}`
- Why fragile: The 2026-08-17 review named this "the single largest cluster of findings", with every non-symbol-reference edge family sharing one root cause — the incremental-refresh path and the full-build path drifting apart despite a doc comment promising they were identical. That cluster was fixed, but the churn has not slowed: eight of the eleven commits since the review touch the graph layer, and `a8a5dda` alone added `graphcallaudit.go` (639 lines) and `graphcallreconcile.go` (73 lines) while rewriting 428 lines of `graphcalls_lsp.go`.
- Safe modification: Any change to a build pass must be mirrored in `internal/server/graphrefresh.go`'s incremental scheduler *in the same commit*, and covered by a test that runs both paths and compares edge sets. Treat the two paths as one unit.
- Test coverage: `internal/server` sits at a 0.75 test/source line ratio; `internal/graph/httproute` at 0.29 for 2,177 lines and was never reviewed at all. The `graph-eval-private` CI job is the real quality gate, and it only runs where the reviewed gold corpus is mounted.

**Four hand-rolled binary formats:**
- Files: `internal/diskstore/diskstore.go` (`MOEDEX03` postings), `internal/diskstore/contentstore.go` (`MOECONT1` shared content store), `internal/graph/diskgraph/diskgraph.go` (graph adjacency), `internal/symbol/codec.go` (symbol sidecar)
- Why fragile: Three independent reviewers found the identical defect class in three of these formats with no knowledge of each other's work — a header-supplied element count used to size an allocation before validating it against the file's actual remaining bytes. One instance was reproduced consuming ~78–82 GB of RSS from a single corrupted byte.
- Safe modification: The bounds-check pattern is now applied and should be copied verbatim for any new table: `internal/diskstore/diskstore.go:728-736` (`count %d exceeds remaining data (%d bytes)`), `internal/graph/diskgraph/diskgraph.go:452-477` and `:860-865`, `internal/symbol/codec.go:210`. Validate the count against `remaining / minItemSize` *before* the `make()`. Never add a length-prefixed table without it.
- Test coverage: Adequate on `diskgraph` and `diskstore`; `internal/graph/diskgraph` is at 0.45 for 1,112 lines, which is thin for a format whose failure mode is an 80 GB allocation.

**Build-tagged arms are not compiled or vetted anywhere in CI:**
- Files: `.gitlab-ci.yml` (the `health` job runs only `make health` = `build vet test`), `Makefile:154` (`build-dense`), `Makefile:176` (`build-simd`), `Makefile:184` (`vet-simd`), `Makefile:194` (`build-lsp`), `Makefile:207` (`test-lsp`), `Makefile:212` (`vet-lsp`)
- Why fragile: 28 files carry `//go:build lsp` and 9 carry `//go:build onnx`. `make health` builds neither. The `build-lsp`/`vet-lsp`/`build-dense`/`vet-simd` targets exist and work, but no CI job invokes them — so a refactor in `internal/navigate`, `internal/server`, or `internal/embed` can break the tagged builds and merge green.
- Safe modification: After touching anything the tagged files import, run `make vet-lsp` and `make build-dense` (or `make vet-simd` to cross-compile the SIMD arm without an amd64 host) locally before pushing.
- Test coverage: The tagged test suites (`make test-lsp`, `make test-dense`) run nowhere in CI. Adding a `vet-lsp` + `vet-simd` job to the `gate` stage is cheap — neither needs a corpus, an LSP server, or the ONNX runtime.

**`graph-eval-private` fails the pipeline when its runner variables are absent:**
- Files: `.gitlab-ci.yml:79-133`
- Why fragile: The job's rules fire on any push or MR touching `internal/graph/**`, `internal/server/**`, `internal/mcp/**`, `internal/eval/**`, `internal/contextwin/**`, `cmd/moedex-serve/**`, `cmd/moedex-index/**`, `Makefile`, or `.gitlab-ci.yml` — a very wide net — and its `before_script` hard-exits 1 if `CI_MOEDEX_GRAPH_EVAL_SHARDS` or `CI_MOEDEX_GRAPH_GOLD` are unset or unreadable. It is tagged `docker-image`, not a dedicated corpus runner, so whether the mounts exist depends on runner configuration rather than job placement.
- Safe modification: Confirm the project variables are set before pushing a change under any of those paths.
- Test coverage: N/A — this is a pipeline-configuration fragility, not a code one. The job's own comment usefully warns that a Verified-precision trip more likely means thin label coverage (the 0.9 floor comes from a 1.0 baseline over ~9 Verified edges) than a real graph regression, so read the labels before treating it as breakage.

**Non-Unix build lock cannot self-heal after a crash:**
- Files: `internal/snapshot/lock_other.go`, `internal/snapshot/lock_unix.go`
- Why fragile: The Unix implementation uses `syscall.Flock`, which the kernel releases on process death. The `!darwin && !linux && ...` fallback uses an `O_CREATE|O_EXCL` lockfile with no PID record, no staleness timeout, and no recovery path — so a crashed or killed build leaves a lockfile that blocks every subsequent build until a human deletes it.
- Safe modification: Unix is the only shipped target today (`deploy/` holds launchd plists and systemd units), so this is latent. But `docs/WINDOWS-SUPPORT.md` scopes a native Windows phase, and that phase must replace this fallback with a `LockFileEx`-based implementation or add PID + staleness recovery.
- Test coverage: `internal/snapshot` is at a 0.25 test/source ratio (226 test lines against 918 source lines) covering atomic publish, cross-process locking, and legacy migration — the lowest-covered package doing genuinely tricky work.

## Scaling Limits

**Single node, no distribution:**
- Current capacity: 491 repos / 63,523 files / 969.5 MB indexed content across 6 shards, built in 59.2 s.
- Limit: `ARCHITECTURE.md` is explicit that distribution and replication are deliberately deferred — the corpus is sharded on disk and served as a multi-shard corpus, but there is no cross-node story. The practical ceiling is one machine's RAM, and the last measured run already peaks at 9.2 GB resident.
- Scaling path: Vertical first (the mmap'd deduped `MOEDEX05` format keeps content off the Go heap, so RAM buys corpus directly). Beyond that, ADR 0001 would need to be revisited.

**Refresh granularity is still shard-level for the legacy path:**
- Current capacity: `internal/blobstore`'s CAS gives per-blob delta at the *storage* layer — `cas-refresh` re-ingests only a changed repo's net-new blobs, leaving co-resident repos untouched.
- Limit: `moedex-index refresh` (the non-CAS path) still rebuilds whole affected shards, and `cas-export` re-exports the entire shard directory rather than appending a changed repo's net-new content and rewriting only affected shards.
- Scaling path: The delta-aware deduped re-export named in `ARCHITECTURE.md`'s deferred list. Compaction-GC (`moedex-index cas-compact`, `internal/blobstore/compact.go`) is already built and is the cheap in-place reclaim for unreferenced content.

**LSP graph construction is rate-limited by design:**
- Current capacity: One global sequential pacer caps request starts at 5/second by default (`MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND`), with a 30-second per-request timeout.
- Limit: A full-corpus `documentSymbol` + `find_references` sweep over 63,523 files at 5 requests/second is the dominant cost of any `-tags lsp` graph build.
- Scaling path: `MOEDEX_GRAPH_LSP_CONCURRENCY` and the rate knob exist; raising them trades language-server stability for build time. Measure before raising — the pacer exists because language servers fall over.

## Dependencies at Risk

**`github.com/modelcontextprotocol/go-sdk v1.7.0`:**
- Risk: New as of `7eae873` (the HEAD commit), pre-2.0, and it drags a wide transitive chain into the default serving binary — `golang.org/x/oauth2`, `github.com/google/jsonschema-go`, `github.com/segmentio/asm` (assembly + `unsafebytes`), `github.com/yosida95/uritemplate/v3`.
- Impact: Breaking changes in a v1.x MCP SDK are likely as the protocol evolves; the blast radius is the entire agent-facing surface (`internal/mcp/sdkserver.go`, `internal/mcp/contracts.go`, `internal/mcp/result.go`).
- Migration plan: `internal/mcp/contract_test.go` (376 lines) pins the tool schemas against the SDK's own types, which is the right hedge — an SDK bump that changes the contract fails there. Keep that test comprehensive and treat it as the upgrade gate.

**`github.com/sugarme/tokenizer` via `replace` to a personal fork:**
- Risk: `go.mod:28` redirects to `github.com/clems4ever/tokenizer v0.0.0-20250926133620-9ddc80533c43` — an untagged pseudo-version of a fork, with no recorded rationale.
- Impact: If that fork disappears or the upstream diverges, the `-tags onnx` dense arm stops building. The fork also brings `github.com/schollz/progressbar/v2` (a v2 line), `github.com/patrickmn/go-cache v2.1.0+incompatible`, and `github.com/mitchellh/colorstring` (last released 2019) as indirects.
- Migration plan: Record the reason for the fork next to the `replace` directive. The dense arm is optional and build-tagged, so the failure mode is contained — but see "build-tagged arms are not compiled in CI": nothing would notice the breakage until someone ran `make build-dense` by hand.

**Go 1.26 with `GOEXPERIMENT=simd`:**
- Risk: `go.mod` requires `go 1.26`, and the SIMD arm needs `GOEXPERIMENT=simd` plus `simd/archsimd`, an experimental package whose API is not covered by the compatibility promise.
- Impact: Contained to `-tags moedex_simd` on amd64. `ARCHITECTURE.md` records the honest measured result: SIMD shows no consistent win at this corpus scale, because the latency tail is scan-bound rather than intersection-bound.
- Migration plan: Given the measured null result, the cheapest path is to keep the arm as a documented experiment and let it break if the experimental API moves. `make vet-simd` cross-compiles it without an amd64 host if you want a cheap CI canary.

## Missing Critical Features

**Branch-aware indexing (ADR 0020, still Proposed):**
- Problem: `internal/corpus/clone.go:162-165` clones with `--depth 1 --single-branch` on the project's default branch. Nothing indexes or serves any other branch.
- Blocks: An agent working on a feature branch gets default-branch content for every retrieval and every context window, silently. For a tool whose entire purpose is supplying accurate context to coding agents, this is the highest-value gap on the list. `docs/plans/0020-branch-aware-indexing.md` has the design; nothing is built.

**Architectural classification is not wired into the build pipeline:**
- Problem: `internal/classify` (598 lines) implements five framework-aware C# classifiers producing `Route`/`Event`/`Queue`/`Table`/`Service` kinds, and `(*symbol.Index).Promote` installs them. `ARCHITECTURE.md` states plainly: "Promotion is in-memory today: nothing in the build pipeline runs the pass yet."
- Blocks: Architectural kinds never reach a persisted sidecar, so no ranking arm or MCP tool can use them. 598 lines of tested, working code that no production path calls.

**Linear reranker has no production caller:**
- Problem: `internal/rank/reranker.go` implements `Fusion`/`LinearReranker`, wired via `Ranker.SetFusion`/`SetReranker`, but nothing opts into `FusionLinear` — RRF remains the only fusion actually used.
- Blocks: Nothing today; noted because it is built-but-unreached code that will rot silently.

**No ANN vector index:**
- Problem: The dense arm does exact or bounded-candidate similarity (`MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT`, `MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES`).
- Blocks: Dense-arm cost grows with corpus size. Documented as deferred in `ARCHITECTURE.md`.

## Test Coverage Gaps

**`internal/corpus/catalog` has zero test files:**
- What's not tested: 517 lines covering the managed-corpus ownership marker (`Catalog`), the acquisition lock (`Lock`), and its parsing/validation — including the advisory lock in `lock.go`.
- Files: `internal/corpus/catalog/catalog.go`, `internal/corpus/catalog/lock.go`
- Risk: This package is imported directly by `internal/ingest/source.go`, `internal/eval/runner_corpus.go`, and `cmd/moedex-parity/main.go` — it is on the engine path, not the ops path. It is also the package whose exec-free property enforces the corpus-isolation invariant, and nothing tests that property. `test.log` confirms `[no test files]`.
- Priority: High.

**`internal/snapshot` at a 0.25 test/source ratio:**
- What's not tested: 918 source lines against 226 test lines, covering atomic publish (`publish.go`, 298 lines), legacy migration (`legacy.go`, 205 lines), and cross-process locking. The `lock_other.go` fallback has no coverage at all.
- Files: `internal/snapshot/publish.go`, `internal/snapshot/legacy.go`, `internal/snapshot/lock_other.go`
- Risk: Atomic-rename and lock code fails in ways that only appear under crash and concurrency — exactly the conditions unit tests must construct deliberately. This package is new (`175a7f8 feat(index): add bounded builds and atomic snapshots`) and was not in scope for the 2026-08-17 review.
- Priority: High.

**`internal/graph/httproute` — 2,177 lines, 0.29 ratio, never reviewed:**
- What's not tested: Route extraction across frameworks; `extract.go` alone is 824 lines.
- Files: `internal/graph/httproute/extract.go`
- Risk: Explicitly listed in the review's "Not covered — nothing here has been cleared, it has been *skipped*" section as the largest excluded chunk. Route edges feed graph queries that agents consume as authoritative.
- Priority: Medium.

**`cmd/moedex-index` at 0.29 for 1,885 lines:**
- What's not tested: The CAS command surface (`cas-build`/`cas-refresh`/`cas-export`/`cas-compact`) and `doctor.go` (which shells out to `launchctl` and probes LSP binaries).
- Files: `cmd/moedex-index/main.go` (994 lines), `cmd/moedex-index/doctor.go`
- Risk: These are the commands that mutate the served corpus in place. The underlying `internal/blobstore` is the best-covered package in the repo at a 2.14 ratio with crash-recovery regression gates (`export_swap_test.go`, `refresh_deduped_swap_test.go`), so the risk is concentrated in CLI wiring and flag handling rather than the storage logic.
- Priority: Medium.

**122 `t.Skip` call sites — most of the suite does not run in default CI:**
- What's not tested: Every LSP integration test (~28 sites gated on `gopls`/`typescript-language-server`/`pyright-langserver`/`csharp-ls`/`cflsp`/`sql-language-server` being on PATH), every ONNX test (gated on `ONNXRUNTIME_LIB_PATH`), every gold-corpus eval (gated on `MOEDEX_CORPUS_ROOT`/`MOEDEX_CORPUS`), every full-corpus parity test (gated on `MOEDEX_CORPUS` + `rg`), and the SIMD differential (gated on AVX2).
- Files: `internal/navigate/*_test.go`, `internal/eval/gold_*_test.go`, `internal/blobstore/*_parity_*_test.go`, `internal/search/parity_test.go`, `internal/server/graphsimilar_onnx_test.go`
- Risk: The env-gated skips are a deliberate and correct design — the alternative is a suite nobody can run. The real gap is that only two of these tiers have a CI job that actually satisfies the gate: `health` installs `ripgrep` so `internal/search`'s rg-backed parity tests run on every push, and `parity`/`graph-eval-private` cover the corpus tiers on suitably provisioned runners. The **LSP and ONNX tiers have no CI job at all**, so ~28 LSP tests and 9 ONNX tests never run anywhere automated.
- Priority: Medium — the same fix as "build-tagged arms are not compiled in CI". Even a job that only runs `make vet-lsp` and `make build-dense` would catch compile-level rot; installing `gopls` in the gate image would additionally light up the Go-language LSP tests for free.

**Review chunks explicitly skipped and never cleared:**
- What's not tested: Beyond `httproute` above — `internal/graph/candidates` (964 lines, 0.74 ratio), `internal/graph/verify` (483 lines, 0.41), `internal/graph/cluster` (406 lines, 0.39), `internal/classify` (598 lines, 0.53), `internal/contextwin` (770 lines, 1.59 ratio but excluded from review), and `scripts/**` (507 lines, the highest-churn excluded chunk at churn 11).
- Files: `CODEBASE-REVIEW.md:58-82` lists all 18
- Risk: `internal/graph/candidates` and `internal/graph/verify` are the recall and precision halves of graph construction and were read only as dependencies by other reviewers, never independently audited.
- Priority: Medium.

---

*Concerns audit: 2026-08-24*
