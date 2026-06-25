# PRODUCTION-ROADMAP — moedex to production

> Companion to [`zoekt-2026-redesign.md`](zoekt-2026-redesign.md) (northstar),
> [`ARCHITECTURE.md`](ARCHITECTURE.md) (what exists today), and the ADRs in
> [`docs/adr/`](docs/adr) (the decisions, with evidence — including the v0.1
> ripgrep-parity contract [`0003`](docs/adr/0003-cox-reduction-ripgrep-parity.md),
> MET, and the query-tail latency work
> [`0012`](docs/adr/0012-search-latency-positional-verify.md)). This document
> enumerates the remaining work to take moedex from "the retrieval + ranking +
> serving spine is built and validated" to "an operable internal service." Every
> claim below is grounded in a file, test, or binary that was read, not in lore.

> **Batch-1 status (2026-06-24): DONE + verified live.** P1 (auth), P2
> (observability), P4 (sidecars), P5 (resilience) implemented and verified on a
> live daemon; P3 **artifacts** (Docker/systemd/CI) done, P3b config-file
> deferred. Delivered via three parallel disjoint-file lanes. Remaining
> follow-ups: deep scan-cancellation (the request timeout is HTTP-layer only —
> `Corpus.Regex/Literal` + `internal/search` carry no `context.Context` yet) and
> a Linux-host validation pass of the images/units (`docker build`,
> `systemd-analyze`).

---

## What "production" means here

Production for moedex is deliberately narrow, and scoping honestly matters:

- **Single node.** No multi-node, no distribution. Blob-SHA content addressing
  leaves room for sharding later, but it is not a v1 requirement.
- **Internal-first.** The first consumer is TurnCommerce engineers and their
  agents, on a trusted internal network — not a public, multi-tenant service.
  This sets the bar for auth/security: "good enough for a trusted-network
  internal tool," not "internet-facing SaaS."
- **~8 GB corpus.** The real corpus is `~/TCGitlab` ≈ 5.2 GB / 484 repos today
  (parity report: `|F|`=60,883 indexed files, 953.6 MB *indexed* content); the
  northstar plans headroom to ~8 GB. A single machine with ~8–16 GB RAM is the
  target envelope.
- **Quality-over-speed, OSS-someday.** The pure-Go, zero-required-dependency
  posture (the dense arm and Zoekt oracle are the only optional externals) is a
  load-bearing identity constraint, not an accident.

"Production" therefore means: an operator can build a shard set, run the warm
daemon as a managed service, observe it, keep it fresh, recover it when it
breaks, and trust who can reach it — for one box, one corpus, one team.

---

## Done / the spine (verified)

The built-and-validated spine — the retrieval core, the four-arm hybrid ranker,
the polyglot symbol layer, the optional dense arm, the agent-context API, the warm
serving spine, shard-level freshness, the content-addressable store, and the
evaluation + parity gates — is described in [`ARCHITECTURE.md`](ARCHITECTURE.md),
with each decision and its evidence recorded in the ADRs under
[`docs/adr/`](docs/adr):

| Area | ADR(s) |
|---|---|
| Scope, memory envelope, pure-Go default | [0001](docs/adr/0001-single-node-scope-pure-go-default.md), [0013](docs/adr/0013-pure-go-defer-simd.md) |
| Retrieval core (positional trigrams, Cox reduction, ripgrep parity) | [0002](docs/adr/0002-positional-trigram-core-byte-offsets.md), [0003](docs/adr/0003-cox-reduction-ripgrep-parity.md) |
| Storage (CAS dedup, mmap compact postings) | [0004](docs/adr/0004-content-addressable-blob-store.md), [0005](docs/adr/0005-mmap-compact-postings.md) |
| Ranking + symbols + dense + agent context | [0006](docs/adr/0006-rrf-hybrid-ranking.md), [0007](docs/adr/0007-optional-dense-arm.md), [0008](docs/adr/0008-polyglot-symbol-sidecar.md), [0009](docs/adr/0009-agent-context-api.md) |
| Serving spine + freshness | [0010](docs/adr/0010-warm-serving-spine.md), [0011](docs/adr/0011-shard-level-freshness.md) |
| Search latency + evaluation gate | [0012](docs/adr/0012-search-latency-positional-verify.md), [0014](docs/adr/0014-eval-harness-gold-gate.md) |

Two headline results these gates produced, which the remaining slices must not
regress: the full-corpus ripgrep-parity gate (`PARITY-REPORT.md`, `make
verify`/`make parity`) records **PASS** — 484/484 repos, |F|=60,883, **AC-D3
1000/1000** (no under-approximation), **AC-D4 0** (no over-approximation); and
query **p95 is 1.443 s**, down from the original 15.3 s
([0012](docs/adr/0012-search-latency-positional-verify.md)). The gold ranking gate
holds production MeanNDCG ~0.93 against a 0.85 floor
([0014](docs/adr/0014-eval-harness-gold-gate.md)).

---

## Remaining slices to production (prioritized)

Ordering rationale: get the daemon **safe and operable** (auth, observability,
ops) before chasing more ranking quality, because the spine is already correct
and fast enough for internal use. Each slice states what+why, rough scope,
dependencies, and a "done when…" criterion.

### Slice P1 — Daemon access control (auth + bind/TLS) — HIGHEST
- **What/why.** The `-http` daemon (`/search`, `/stats`, `/healthz`) has **no
  authentication, no TLS, and no rate limiting** — confirmed by grepping
  `cmd/moedex-serve/main.go` (no `Authorization`, `TLS`, `ListenAndServeTLS`,
  `BasicAuth`). For an internal tool this is the single biggest gap before it
  leaves a laptop: anyone who can reach the port can read the entire source
  corpus. (MCP runs over stdio so it inherits the parent process's trust; HTTP
  is the exposed surface.)
- **Scope.** A static bearer token / shared-secret header check (env-supplied),
  configurable bind address (default `127.0.0.1` not `0.0.0.0`), and optional
  TLS via `ListenAndServeTLS` with cert/key flags. Reverse-proxy-friendly
  (document "terminate TLS + authn at an internal proxy" as the alternative).
- **Dependencies.** None; standalone in `cmd/moedex-serve`.
- **Done when.** `/search` returns 401 without the configured token; the daemon
  binds to loopback by default; TLS can be enabled by flag; a handler test
  covers the auth path.

### Slice P2 — Observability (structured logs, metrics, request logging)
- **What/why.** Logging today is `fmt.Fprintf(os.Stderr, …)` only; there is **no
  structured logging, no `/metrics`, and no per-request access log** on the HTTP
  handlers. You cannot tell, in production, how many queries ran, how slow they
  were, what error rate the daemon has, or whether a reload succeeded — except by
  eyeballing stderr.
- **Scope.** Adopt `log/slog` (stdlib, no new dep) across the binaries; add a
  request-logging middleware on the HTTP mux (method, path, query class, match
  count, latency, status); expose a minimal `/metrics` (counts + latency
  histograms — can be plain-text/Prometheus-format via stdlib, or a single small
  dep if OSS posture allows). Emit reload success/failure and shard-count gauges.
- **Dependencies.** Light coupling with P1 (instrument the same handlers once);
  do P1 first or together.
- **Done when.** Every `/search` produces one structured log line with latency
  and result count; `/metrics` reports request count, error count, and a latency
  histogram; reload events are observable.

### Slice P3 — Deployment, config, and service management
- **What/why.** There is **no Dockerfile, no systemd unit, no CI config, no
  install script, and no config file** (confirmed: flags + env vars only). An
  operator has to hand-roll how moedex starts, restarts, and reads its settings.
  This is the gap between "a binary I run in a terminal" and "a service."
- **Scope.** (a) A Dockerfile (and a `-dense` variant pulling the ONNX runtime)
  + a systemd unit template; (b) a config-file path (`-config foo.yaml`/env)
  layered under flags so shard-dir, bind addr, embed mode, token, and corpus root
  live in one declarative place; (c) a CI pipeline (`.gitlab-ci.yml` — note the
  repo lives on internal GitLab) running `make health` on every push and `make
  parity` on a schedule/tagged build; (d) document the steady-state loop
  (`moedex-index refresh && kill -HUP`) as a cron unit.
- **Dependencies.** Benefits from P1/P2 (config carries the token; CI runs the
  gates). Config-file work touches every binary's flag parsing.
- **Done when.** `docker run` (or a systemd unit) starts a queryable daemon from
  a config file; CI runs `make health` green on push; the refresh-cron is
  documented and runnable.

### Slice P4 — Serving boot/reload cost: persist & reuse BM25 + symbol sidecars
- **What/why.** `RankCorpus.OpenRank` **rebuilds the token index
  (`tokenindex.Build`) and the symbol index (`symbol.BuildMulti`) from scratch on
  every boot and every SIGHUP** (`rankcorpus.go:95,125`). Only embeddings are
  persisted. The `TKI1` (`tokenindex/codec.go`) and `SYM1` (`symbol/codec.go`)
  codecs already exist but are **not wired into serving** — so a hot-reload pays
  full BM25 tokenization + 5-language symbol extraction over the whole corpus
  every time, defeating much of the point of "swap shards and SIGHUP." This is a
  freshness/latency gap, not a correctness one.
- **Scope.** Persist token + symbol sidecars next to the shards (mirroring the
  embedding-sidecar fingerprint-validation pattern already in `rankcorpus.go`),
  load them when the shard fingerprint matches, rebuild only on mismatch. Have
  `moedex-index build/refresh` produce these sidecars so the daemon never builds
  them.
- **Dependencies.** None blocking; reuses existing codecs and the existing
  fingerprint/meta pattern.
- **Done when.** A SIGHUP over an unchanged shard set reloads BM25 + symbol
  layers from sidecars instantly (no rebuild), validated by a boot-time log/test;
  a changed shard set rebuilds and re-persists.

### Slice P5 — HTTP resilience hardening (timeouts, panic recovery)
- **What/why.** The MCP path is hardened (timeout, panic recovery, caps), but the
  **HTTP handlers have no panic recovery and no per-request timeout** — a panic in
  `/search` crashes the daemon, and a degenerate query (e.g. the parked
  `\p{Greek}`/sub-trigram tail that can still take ~10 s) can tie up a handler
  with no deadline. For an internal multi-user daemon this is a self-DoS risk.
- **Scope.** A recovery middleware on the mux (mirror `mcp.handleSafe`), a
  per-request `context.WithTimeout` plumbed into `Corpus.Regex/Literal` (the
  search path is already context-aware in the dense arm — extend it), and
  `http.Server` read/write/idle timeouts. Optionally cap concurrent in-flight
  searches.
- **Dependencies.** Pairs naturally with P1/P2 (same middleware stack). The
  per-query timeout leans on the parked latency tail — a deadline is the pragmatic
  cap for those degenerate shapes.
- **Done when.** A panicking handler returns 500 and the daemon survives; a query
  exceeding the deadline returns a clean error; `http.Server` timeouts are set.

### Slice P6 — Scale validation toward the full ~8 GB corpus
- **What/why.** Parity is proven at 953.6 MB *indexed* / ~5.2 GB raw, with build
  peak RSS **6.4 GB** and run peak **6.8 GB** (`PARITY-REPORT.md`). The northstar
  targets ~8 GB. `cmd/scale` measures in-memory sizing and `MOEDEX_MMAP=1` heap,
  but there is **no validated headroom run** at the 8 GB / served (mmap) profile,
  and the parity build itself is already at ~6.8 GB RSS — the memory envelope is
  the open frontier the northstar names ("a known scaling frontier").
- **Scope.** Run `make parity` / `cmd/scale` against a corpus padded toward 8 GB
  (or the real corpus as it grows); record served-mode (mmap) RSS, query latency,
  and dense-store size; confirm no OOM and that the daemon's working set is the
  mmap'd postings, not the heap. Decide the box's RAM spec from the numbers.
- **Dependencies.** None; tooling exists. Informs P3's deployment sizing.
- **Done when.** A parity/scale run at the target corpus size completes without
  OOM, with served-mode RSS and latency recorded in an updated report and a
  recommended hardware envelope.

### Slice P7 — Refresh correctness at scale + manifest robustness
- **What/why.** Incremental refresh exists and is unit-tested, but its strategy is
  "correct-but-simpler": it re-ingests **every repo that shares an affected
  shard** (not just changed repos), reassigns shard numbering, and loses
  cross-shard dedup between carried and rebuilt shards (documented caveats in
  `manifest.go`). At 484 repos / 6 shards, one changed repo can trigger
  re-ingesting ~80 co-resident repos. This is acceptable but unvalidated at scale
  and worth a measured pass before it's a daily cron.
- **Scope.** Measure a real `refresh` against the live corpus (one changed repo →
  how many re-ingested, wall time, correctness vs a full rebuild); consider a
  finer repo→shard mapping if the blast radius is too large; verify the
  atomic-swap + SIGHUP loop end-to-end under a concurrent query load.
- **Dependencies.** Builds on P4 (sidecars must refresh too) and P6 (scale data).
- **Done when.** A measured refresh-then-SIGHUP cycle on the real corpus serves
  the updated content, matches a full rebuild on a spot battery, and its blast
  radius/time is recorded.

### Slice P8 — Gold-set expansion + UDCG soft-gating
- **What/why.** The eval harness, UDCG metric, and a real NDCG gate all exist, but
  the gold set is **42 queries** (36 in the hard gate) — small for a confident
  ranking signal, and the research notes (`learned-reranker.md`) call for 50–100.
  UDCG is implemented and tested as a *metric* but is **not yet a gate** (the
  hard gate asserts on NDCG/Recall/MRR, not UDCG); hard distractors are labeled
  but only measured. This is the prerequisite for any future reranker work.
- **Scope.** Grow the pooled gold set toward ~80–100 queries across the existing
  strata (filename-aligned, non-aligned, synonym-gap) with the documented
  two-annotator + LLM-judge protocol; add UDCG as a *soft* reported gate (warn,
  don't fail) until it's calibrated, then promote to a hard floor. Keep the
  no-regression discipline already in `gold_gate_test.go`.
- **Dependencies.** None blocking; it's the gating dependency for any Stage-2
  reranker (`learned-reranker.md`).
- **Done when.** The gold set is ≥80 queries with documented labels; UDCG is
  reported by the harness on every gold run; the NDCG hard floor still holds.

### Slice P9 — Docs & onboarding for operators
- **What/why.** `ARCHITECTURE.md`, the `cmd/moedex-serve/README.md`, and the spec
  docs are strong, but they describe the *engine*, and `ARCHITECTURE.md`'s
  "current state" section is **stale** (it still says symbols are Go-only, the
  dense arm needs an HTTP server, and there's no path arm — all since superseded:
  5-language symbols, in-process ONNX, path arm on by default). There is no
  single "stand up and operate moedex" runbook.
- **Scope.** Refresh `ARCHITECTURE.md`'s current-state/deferred sections to match
  the code (path/symbol/dense arms, ONNX, freshness, serving spine); add an
  operator runbook (build a shard dir → run the daemon → configure auth/embed →
  refresh loop → observe → recover) — ideally the first content under a `docs/`
  dir if the doc set grows.
- **Dependencies.** Should land after P1–P5 so the runbook documents the real
  auth/config/observability surface.
- **Done when.** `ARCHITECTURE.md` matches the code, and a from-zero operator can
  follow one doc to a queryable, authenticated, observable daemon.

### Slice P10 — Dense model selection (research lever 3, deferred)
- **What/why.** The dense arm ships one embedder
  (`st-codesearch-distilroberta-base`, ~2021). `research/code-embedders.md` surveys
  newer (2025–2026) candidates and shortlists ones to A/B (top pick: IBM
  `granite-embedding-english-r2`, Apache-2.0, 768-d encoder that drops into the
  in-process path); the `embed` package already supports A/B via
  `NewONNXEmbedderFromFiles(...)`. Choosing a better code embedder is a quality
  lever, explicitly lower priority than operability and gated on the eval set.
- **Scope.** A/B the shortlisted candidates from `research/code-embedders.md`
  on the (expanded, P8) gold set via the existing
  `NewONNXEmbedderFromFiles` seam and `gold_onnx_test.go` harness; promote a
  winner only if it beats the current model on UDCG/NDCG by more than judge noise.
- **Dependencies.** **Blocked on P8** (need a bigger, UDCG-instrumented gold set
  to choose responsibly). Below everything operational.
- **Done when.** A documented A/B picks the production embedder on gold-set
  evidence, or explicitly confirms the current model stays.

---

## Explicitly deferred (not on the production path)

These are tracked in the northstar / `research/` and are **not** required for an
internal single-node v1; listed so the roadmap is honest about scope:

- **Multi-node / distribution / sharding across machines** — `zoekt-2026-redesign.md`.
- **SIMD intersection/verification kernel** — `research/simd-kernel.md`
  (ADR [`0012`](docs/adr/0012-search-latency-positional-verify.md) /
  [`0013`](docs/adr/0013-pure-go-defer-simd.md) show the tail is scan-bound, not
  intersection-bound, so this is low-leverage now).
- **FM-index compressed cold tier** — `research/fm-index-cold-tier.md`.
- **Learned reranker (GBDT / cross-encoder)** — `research/learned-reranker.md`;
  staged behind the gold-set/UDCG work (P8) and explicitly "only if eval shows
  headroom."
- **ANN vector index** — current dense store is brute-force flat (`embed.Store`);
  fine at this corpus size, an optimization behind the same `Search` API.
- **find-refs / go-to-def** — the symbol layer scopes blocks and powers a ranking
  arm; cross-reference graph is future work.
