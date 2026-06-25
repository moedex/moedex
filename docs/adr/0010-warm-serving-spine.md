# ADR 0010: Warm multi-shard serving spine — mmap retrieval daemon + ranked context, with sidecar persistence and daemon hardening

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
The one-shot CLIs (`moedex`, `moedex-mcp`) rebuild an index per invocation — fine for a single repo, fatal for whole-corpus latency: re-ingesting and re-indexing 5.2 GB on every query is a non-starter, and rebuilding the BM25 token index + 5-language symbol layer on every boot/reload defeats the point of a warm process. Production for moedex means an operator can build a shard set once and run a warm daemon that answers with zero cold-start — on a trusted internal network, so the bar is "good enough for a trusted-network internal tool," not internet-facing SaaS.

## Decision
Split offline build from online serving. `cmd/moedex-index` produces/refreshes a **prebuilt shard directory**; the `cmd/moedex-serve` daemon **only ever reads it**, mmap'd and warm.

- **Retrieval corpus** (`server.Corpus`): `Open` mmaps every `*.idx` shard once and holds the mappings for its lifetime (postings off-heap — [0005](./0005-mmap-compact-postings.md)); `Regex`/`Literal` fan a per-shard scan across shards (bounded by `NumCPU`) and merge by repo/relpath/line — no cross-shard blob-ID space to reconcile because matches carry absolute/repo paths. Backs `-http` (`/search`) and `-q`.
- **Ranked context** (`server.RankCorpus`): `OpenRank` loads only blob *content* across shards into one content-only unified index with **global blob IDs**, so BM25 IDF and RRF fusion are corpus-wide, not per-shard. Implements `mcp.ContextSearcher`, backing `-mcp` ([0009](./0009-agent-context-api.md)).
- **Sidecar persistence** is **load-or-build-and-save**, fingerprint-validated: the BM25 token index (`TKI1`), symbol index (`SYM1`), and (when an embedder is set) embedding store (`MDXE`) are reused if their `.meta` validator matches the corpus fingerprint, else rebuilt and re-persisted. `BuildSidecars` lets the offline indexer pre-warm token+symbol caches so a hot-reload over an unchanged shard set is instant.
- **Daemon hardening** (`-http`): optional bearer auth (`MOEDEX_AUTH_TOKEN`; `/healthz`,`/metrics` stay open), **loopback-default bind** when no token is set, optional TLS, `log/slog` + `/metrics`, panic-recovery + per-request-timeout middleware, bounded server timeouts, and **SIGHUP hot-swap** of the served corpus without dropping in-flight requests (a failed reload keeps the current one).

## Consequences
**Positive**
- Zero cold-start serving over the whole corpus; corpus-wide ranking (not per-shard) because blob IDs are unified.
- A SIGHUP swaps shards live; persisted sidecars mean a reload over unchanged shards pays no rebuild.
- The exposed `-http` surface is defensible on an internal network (auth, loopback default, TLS, timeouts, panic recovery).

**Negative / costs**
- Freshness is shard-level, not per-request live ([0011](./0011-shard-level-freshness.md)); the daemon serves whatever the last `moedex-index build/refresh` produced.
- A best-effort sidecar cache write that fails never fails the boot — correct, but means a transient disk error silently costs a rebuild next time.
- The dense embedder for `-mcp` is chosen at boot (`-embed auto|onnx|http|none`); changing it is a restart/reload, not a runtime toggle.

## Evidence
SIGHUP hot-reload tested (`reload_test.go`) and validated under load: 400 concurrent requests across a live shard swap returned **0 non-200s** ([0011](./0011-shard-level-freshness.md)). Served-mode resident set ≈ 3.79 GB on the 5.2 GB corpus with warm grep p95 563 ms ([0001](./0001-single-node-scope-pure-go-default.md), [0005](./0005-mmap-compact-postings.md)). MCP hardening covered by `hardening_test.go`.

## Related
[0004](./0004-content-addressable-blob-store.md), [0005](./0005-mmap-compact-postings.md), [0009](./0009-agent-context-api.md), [0011](./0011-shard-level-freshness.md).
