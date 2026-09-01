<!-- refreshed: 2026-08-24 -->
# Architecture

**Analysis Date:** 2026-08-24

## System Overview

```text
┌─────────────────────────────────────────────────────────────┐
│                    Entry points (cmd/)                       │
├──────────────────┬──────────────────┬───────────────────────┤
│   moedex-serve   │   moedex-index   │    moedex-corpus      │
│ `cmd/moedex-serve`│ `cmd/moedex-index`│  `cmd/moedex-corpus` │
│ warm daemon:     │ offline shard/CAS │ glab/git acquisition  │
│ -http -mcp -q    │ builder + graph   │ (quarantined)         │
└────────┬─────────┴────────┬─────────┴──────────┬────────────┘
         │                  │                     │
         ▼                  ▼                     ▼
┌─────────────────────────────────────────────────────────────┐
│              Serving spine + orchestration                   │
│  `internal/server`  (Corpus, RankCorpus, SymbolCorpus,       │
│                      GraphToolset, BuildSidecars/BuildGraph) │
│  `internal/mcp`     (tool contracts, SDK server, stdio/HTTP) │
└────────┬───────────────────────┬────────────────────┬───────┘
         │                       │                    │
         ▼                       ▼                    ▼
┌────────────────┐  ┌──────────────────────┐  ┌───────────────┐
│ Retrieval      │  │ Ranking + context     │  │ Graph layer   │
│ `internal/     │  │ `internal/rank`       │  │ `internal/    │
│  search`       │  │ `internal/contextwin` │  │  graph/*`     │
│ `internal/     │  │ `internal/tokenindex` │  │ candidates →  │
│  query`        │  │ `internal/embed`      │  │ verify →      │
│ (Cox reduction)│  │ `internal/symbol`     │  │ diskgraph     │
└────────┬───────┘  └───────────┬───────────┘  └───────┬───────┘
         │                      │                      │
         ▼                      ▼                      ▼
┌─────────────────────────────────────────────────────────────┐
│                    Core index primitives                     │
│  `internal/index` (Blob/Posting/FileRef) · `internal/trigram`│
│  `internal/fold` · `internal/setops`                         │
└────────────────────────────┬────────────────────────────────┘
                             ▼
┌─────────────────────────────────────────────────────────────┐
│                 Storage / on-disk formats                    │
│  `internal/diskstore` (MOEDEX03/05, MOECONT1, mmap)          │
│  `internal/blobstore` (MOEBLOB1 CAS) · `internal/snapshot`   │
│  `internal/ingest` (git ls-files, .ai-privacy.yml gate)      │
└─────────────────────────────────────────────────────────────┘
```

## Component Responsibilities

| Component | Responsibility | File |
|-----------|----------------|------|
| trigram | Positional-trigram primitive (`N = 3`, `Trigram [3]byte`) | `internal/trigram/trigram.go` |
| fold | ASCII case-fold variants shared by Cox reduction and verify prefilter | `internal/fold/fold.go` |
| index | In-memory content-addressed trigram index; SHA dedup; sorted `Posting{Blob,Offset}` | `internal/index/index.go` |
| index (codec) | Varint-delta posting encode/decode; lazy restore | `internal/index/codec.go`, `internal/index/snapshot.go` |
| ingest | Read a repo's privacy-eligible tracked text files via `git ls-files -s -z` | `internal/ingest/ingest.go`, `internal/ingest/source.go` |
| query | Regex → necessary-condition boolean trigram query (Cox reduction) | `internal/query/query.go` |
| search | Candidate retrieval + real-regex verify → line matches | `internal/search/search.go` |
| setops | Sorted-set intersect/union kernel (pure-Go default, SIMD behind a tag) | `internal/setops/setops.go` |
| diskstore | Persist/reload index; mmap postings; deduped served format + shared content store | `internal/diskstore/diskstore.go`, `internal/diskstore/dedupstore.go`, `internal/diskstore/contentstore.go` |
| blobstore | Corpus-wide CAS (each unique blob stored once), per-blob delta refresh, shard export | `internal/blobstore/blobstore.go`, `internal/blobstore/refresh_deduped.go`, `internal/blobstore/export_deduped.go` |
| tokenindex | Persistent BM25 term statistics; the frozen `Tokenize` rule | `internal/tokenindex/tokenindex.go` |
| symbol | Polyglot syntactic symbol layer + corpus-wide name lookup | `internal/symbol/symbol.go`, `internal/symbol/build_multi.go`, `internal/symbol/corpus.go` |
| classify | Promote syntactic symbol kinds to architectural ones from C# framework markers | `internal/classify/classify.go` |
| embed | Optional dense arm: chunk → vector → cosine search | `internal/embed/embed.go`, `internal/embed/onnx.go` |
| rank | Fuse lexical/path/symbol/dense arms via Reciprocal Rank Fusion | `internal/rank/ranker.go` |
| contextwin | Assemble ranked results into token-budgeted, symbol-scoped blocks | `internal/contextwin/contextwin.go` |
| graph | Shared four-tier confidence + evidence value types | `internal/graph/model.go` |
| graph/candidates | Recall-complete cross-shard edge candidates (trigram fan-out ∪ symbol refs) | `internal/graph/candidates/` |
| graph/verify | Language-aware regex tier: Candidate → Pattern promotion | `internal/graph/verify/` |
| graph/diskgraph | Offline-built, mmap-served adjacency keyed by (blob SHA, symbol offset) | `internal/graph/diskgraph/diskgraph.go` |
| server | Warm multi-shard serving spine + graph build/query orchestration | `internal/server/corpus.go`, `internal/server/rankcorpus.go`, `internal/server/graphtools.go` |
| mcp | Typed MCP tool contracts, SDK server over stdio + Streamable HTTP | `internal/mcp/mcp.go`, `internal/mcp/sdkserver.go`, `internal/mcp/contracts.go` |
| snapshot | Atomic, immutable index-generation bundle (`snapshot.json`, `CURRENT`) | `internal/snapshot/snapshot.go`, `internal/snapshot/publish.go` |
| navigate | Optional LSP-precise navigation arm (`-tags lsp`) over out-of-process servers | `internal/navigate/lsp.go`, `internal/navigate/pool.go` |
| parity | Full-corpus ripgrep parity harness + shard-level freshness manifest | `internal/parity/run.go`, `internal/parity/manifest.go` |
| eval | IR-metrics + gold-gate harness for ranker and graph quality | `internal/eval/` |
| corpus | Corpus acquisition/freshness over glab/git — never imported by the engine | `internal/corpus/clone.go`, `internal/corpus/sync.go` |
| corpus/catalog | Leaf managed-corpus schema (marker + lock), zero `os/exec` | `internal/corpus/catalog/` |

## Pattern Overview

**Overall:** Layered pipeline architecture with a strict acyclic package DAG. Two
pipelines (offline build, online serve) meet at an on-disk format boundary;
optional capabilities are compile-time arms behind build tags.

**Key Characteristics:**
- **Single Go module, `internal/`-only library code.** `module moedex`; every
  library package is under `internal/` and every executable is a thin `main` in
  `cmd/`. Nothing is importable outside the module.
- **Layered DAG, no cycles.** Leaf primitives (`trigram`, `fold`, `setops`,
  `snapshot`, `graph`, `corpus/catalog`) depend on nothing; `index` sits above
  them; `server` is the single fat orchestrator at the top that composes
  everything. The full import graph is acyclic.
- **Offline build / online serve split.** `cmd/moedex-index` writes shard
  directories, CAS packs, sidecars, and the graph; `cmd/moedex-serve` only ever
  reads them. The daemon never builds a shard.
- **Optional capability = build tag + `_disabled.go` stub.** ONNX (`onnx`), LSP
  navigation (`lsp`), and the SIMD kernel (`moedex_simd`) each ship a real
  implementation file and a same-API stub selected by `//go:build !tag`, so the
  default build is pure Go standard library.
- **Necessary-condition retrieval, then verify.** Trigram candidate generation
  may over-approximate but must never under-approximate; a real regex engine
  verifies every candidate.
- **Content addressed by git blob SHA.** Identical content is indexed once
  corpus-wide; blob SHA is the cache key at every layer.
- **Off-heap serving.** Postings and blob content stay in mmap'd regions; the Go
  heap holds directories and indexes, not corpus bytes.

## Layers

**Primitives (leaves):**
- Purpose: value types and pure algorithms with no dependencies
- Location: `internal/trigram`, `internal/fold`, `internal/setops`,
  `internal/graph`, `internal/snapshot`, `internal/fmindex`,
  `internal/corpus/catalog`, `internal/version`
- Contains: constants, enums, small kernels, on-disk schema structs
- Depends on: nothing inside `moedex/`
- Used by: every layer above

**Core index:**
- Purpose: the in-memory content-addressed trigram index
- Location: `internal/index`
- Contains: `Blob`, `FileRef`, `Posting`, `Index`, posting codec, snapshot/restore
- Depends on: `internal/trigram`
- Used by: `search`, `query`, `rank`, `symbol`, `tokenindex`, `embed`,
  `diskstore`, `classify`, `graph/candidates`, `graph/verify`, `server`

**Acquisition & persistence:**
- Purpose: get bytes in, get bytes to disk, get bytes back mmap'd
- Location: `internal/ingest`, `internal/diskstore`, `internal/blobstore`
- Contains: git enumeration + privacy gating, binary-format writers/readers, CAS
- Depends on: `index`, `trigram`, `corpus/catalog`, and (for `blobstore`) `parity`
- Used by: `server`, `parity`, `cmd/moedex-index`

**Retrieval:**
- Purpose: turn a pattern into line matches at ripgrep parity
- Location: `internal/query`, `internal/search`
- Contains: Cox reduction, candidate set-ops, line filters, parallel verify
- Depends on: `index`, `trigram`, `fold`, `setops`
- Used by: `server`, `parity`, `classify`, `graph/httproute`, `cmd/moedex`

**Ranking & context:**
- Purpose: rank blobs for an agent query and pack source into a token budget
- Location: `internal/rank`, `internal/tokenindex`, `internal/embed`,
  `internal/symbol`, `internal/contextwin`
- Contains: RRF fusion, BM25, arm gates, symbol extraction, block assembly
- Depends on: `index`, `query`
- Used by: `mcp`, `server`

**Graph:**
- Purpose: cross-shard relationship edges with provenance-backed confidence
- Location: `internal/graph`, `internal/graph/candidates`,
  `internal/graph/verify`, `internal/graph/diskgraph`, `internal/graph/httproute`,
  `internal/graph/cluster`, `internal/graph/manifest`
- Contains: candidate generation → regex verification → mmap'd adjacency
- Depends on: `index`, `symbol`, `query`, `trigram`, `graph`
- Used by: `server`, `mcp` (for the shared confidence enum)

**Serving & protocol:**
- Purpose: warm multi-shard serving and the agent-facing tool surface
- Location: `internal/server`, `internal/mcp`
- Contains: `Corpus`/`RankCorpus`/`SymbolCorpus`/`GraphToolset`, tool contracts,
  SDK server, snapshot identity
- Depends on: everything below
- Used by: `cmd/moedex-serve`, `cmd/moedex-index`, `cmd/moedex-mcp`

**Quarantined operations:**
- Purpose: acquire and refresh the corpus over glab/git
- Location: `internal/corpus` (driven by `cmd/moedex-corpus`)
- Contains: `Runner` shell-out seam, doctor/clone/sync/reindex
- Depends on: `internal/corpus/catalog` only
- Used by: `cmd/moedex-corpus` only — **never** by the engine or the daemon

## Data Flow

### Primary Request Path

Agent MCP `search_context` over the warm daemon:

1. Transport accepts the call — stdio or Streamable HTTP (`internal/mcp/sdkserver.go:34` `configureOfficial`, `internal/mcp/sdkserver.go:179` `officialHTTPHandler`)
2. Tool dispatch validates arguments against the process-stable contract (`internal/mcp/mcp.go:309`, `internal/mcp/contracts.go:206`)
3. `RankCorpus.SearchContext` runs the ranked path over the concatenated content index (`internal/server/rankcorpus.go`)
4. `Ranker.Rank` fuses up to four arms via RRF (`RRFk` = 60) into `[]RankedResult` (`internal/rank/ranker.go`)
   - lexical arm: trigram candidates + BM25 (`K1` = 1.2, `B` = 0.75)
   - path arm: filename-token coverage ≥ `PathMinCoverage` (0.6), on by default
   - symbol arm: symbol-name coverage ≥ `SymbolMinCoverage` (0.67), needs a symbol index
   - dense arm: cosine over chunk embeddings, gated by `DenseMinQueryTerms` (5)
5. `contextwin.Assemble` expands each salient `LineSpan` to its enclosing symbol block, merges near-adjacent blocks, demotes import-heavy and test blocks, and packs best-first under `DefaultTokenBudget` = 8000 (`internal/contextwin/contextwin.go:38`)
6. `GraphToolset.Neighbors` anchors each block by path + line range and walks six directed lanes over one reverse mmap sweep per hop (`internal/server/graphneighbors.go:55`)
7. Result is stamped with snapshot identity and returned as `structuredContent` (`internal/mcp/result.go`)

### Retrieval Path (`-http` / `-q` / `moedex` CLI)

1. `search.Regex` / `search.Literal` parse the pattern (`internal/search/search.go:86`, `internal/search/search.go:208`)
2. `query.FromRegexp` reduces it to a necessary-condition boolean trigram query (`internal/query/query.go`)
3. The query evaluates over posting lists into a candidate blob set (`internal/setops`)
4. Candidate lines are prefiltered by required literals / rune classes, then verified by Go RE2 (`internal/search/search.go:355` `verifyCandidateLines`)
5. `server.Corpus.Regex` fans the per-shard scan across shards bounded by `NumCPU` and merges by concatenation — `search.Match` carries absolute/repo/relative paths, so there is no cross-shard blob-ID space to reconcile (`internal/server/corpus.go`)

### Index Build Path

1. `ingest.Repo` reads `.ai-privacy.yml` (missing/empty ⇒ level 3, invalid ⇒ fail closed), then shells out to `git -C <dir> ls-files -s -z`, taking git's blob SHA as content identity (`internal/ingest/ingest.go`)
2. Binary blobs (containing NUL) are skipped; a leading UTF-8 BOM is stripped to stay aligned with ripgrep line boundaries
3. `index.AddFile` dedups by SHA — a repeat SHA only appends a `FileRef`; new content emits every positional byte-trigram as `Posting{Blob, Offset}`, already sorted because blobs arrive in increasing ID order and offsets in increasing order (`internal/index/index.go:165`)
4. Sidecars build from the same in-memory index: `tokenindex.Build` (BM25), `symbol.BuildMulti` (per-extension extractor dispatch), optionally `embed.BuildStore`
5. `diskstore.Save` / `blobstore.ExportDedupedShardDir` write the shard dir; `LoadMmap` / `LoadMmapDeduped` map it back with postings left on disk

### Graph Build Path

1. `graph/candidates.GenerateAll` unions symbol-layer classified references with a trigram fan-out over every shard, pairing each occurrence with each same-name definition — recall-complete, resolving nothing (`internal/graph/candidates/`)
2. `graph/verify.Verify` applies language-aware call/import/type patterns with comments and string literals masked; confirmed edges become `Pattern` (0.6), unconfirmed remain `Candidate` (0.3) — the precision pass never costs recall
3. `server.BuildGraph` widens call sites to their enclosing symbol and writes `corpus-graph.graph` via `diskgraph.Builder` (`internal/server/graphbuild.go`)
4. Under `-tags lsp` an additional pass emits `Proven` CALLS; under `-tags onnx` a similarity pass emits `SIMILAR_TO` edges carrying exact cosine

**State Management:**
- Server state is an immutable, atomically swapped generation. `GraphToolset.Reload`
  installs a freshly mmap'd graph while in-flight calls drain against the old one
  (`internal/server/graphtools.go:440`); `cmd/moedex-serve/reload.go` does the same
  for the corpus on SIGHUP, and a failed reload keeps the current generation.
- Derived sidecars are load-or-build-and-save caches validated by a `.meta`
  fingerprint (shard set + blob count, plus `symbol.ExtractorsVersion` for symbols
  and model + chunk geometry for embeddings) — `internal/server/rankcorpus.go:607`.
- No shared mutable process state in the request path.

## Key Abstractions

**Content identity (git blob SHA):**
- Purpose: the single key that makes dedup, delta refresh, and cache validation work
- Examples: `internal/index/index.go` (`Blob.SHA`), `internal/blobstore/blobstore.go`, `internal/diskstore/contentstore.go`, `internal/graph/diskgraph/diskgraph.go` (`Key{BlobSHA, SymbolOffset}`)
- Pattern: opaque variable-length key (SHA-1 today, SHA-256-ready); never parsed, only compared

**Necessary-condition query (`query.Query`):**
- Purpose: a boolean trigram expression that may over-select but never under-selects
- Examples: `internal/query/query.go:35`
- Pattern: interface with `Eval` + `String`; `All`, `And`, `Or` constructors

**Positional posting (`index.Posting`):**
- Purpose: `{Blob, Offset}` in **byte** offsets, kept sorted without an explicit sort
- Examples: `internal/index/index.go:25`
- Pattern: append-only emission in increasing (blob, offset) order

**Confidence tier (`graph.ConfidenceTier`):**
- Purpose: provenance-backed edge strength, ordered weakest → strongest
- Examples: `internal/graph/model.go` — `Candidate` (0.3), `Pattern` (0.6), `Verified` (0.85), `Proven` (1.0); `DefaultMinConfidence = Pattern`
- Pattern: compact ordinal on disk, name-marshalled in JSON, score derived at API boundaries and never persisted independently

**Pluggable extractor (`symbol.Extractor`):**
- Purpose: per-language symbol extraction dispatched by file extension
- Examples: `internal/symbol/extract.go:41`, dispatch in `internal/symbol/build_multi.go:21`
- Pattern: interface + optional `RefExtractor` / `DefsRefsExtractor` upgrades, discovered by type assertion; unknown extension ⇒ nil ⇒ brace/indent fallback

**Build-tag capability pair:**
- Purpose: an optional dependency that must not touch the default build
- Examples: `internal/embed/onnx.go` / `internal/embed/onnx_disabled.go`;
  `internal/navigate/lsp.go` / `internal/navigate/lsp_disabled.go`;
  `internal/server/graphsimilar_onnx.go` / `internal/server/graphsimilar_disabled.go`;
  `internal/setops/setops_simd_amd64.go` / `internal/setops/setops_fallback.go`
- Pattern: identical exported API in both files, a `const XCompiled bool` capability
  flag, and a package-level sentinel error telling the caller which tag to rebuild with

**Shell-out seam (`corpus.Runner`):**
- Purpose: make every glab/git invocation unit-testable without a network
- Examples: `internal/corpus/runner.go:44`, `internal/corpus/clone.go`
- Pattern: interface injected at the call site; `ExecRunner` is the only real implementation

**Tool handler (`mcp.ToolHandler` / `mcp.ContextSearcher`):**
- Purpose: let `internal/server` supply tools without `internal/mcp` importing it
- Examples: `internal/mcp/mcp.go:54`, `internal/mcp/mcp.go:111`, implemented by `internal/server/graphtools.go:412`
- Pattern: consumer-side interface — dependency inversion keeps `mcp` below `server` in the DAG

## Entry Points

**`moedex-serve` (warm daemon):**
- Location: `cmd/moedex-serve/main.go:51`
- Triggers: operator/systemd/launchd; `-http`, `-mcp`, `-mcp-http`, `-q`, `-build-embeddings`
- Responsibilities: resolve `-config` env file before flag defaults (flag > file > env > default), open a shard dir or snapshot `CURRENT`, serve, hot-swap on SIGHUP

**`moedex-index` (offline builder):**
- Location: `cmd/moedex-index/main.go:83`
- Triggers: CLI subcommand dispatch — `build`, `check`, `refresh`, `graph`, `graph-audit`, `cas-build`, `cas-refresh`, `cas-export`, `cas-compact`, `doctor`, `snapshot-*`
- Responsibilities: produce and refresh shard dirs, the CAS, sidecars, and the graph

**`moedex-corpus` (acquisition):**
- Location: `cmd/moedex-corpus/main.go:38`
- Triggers: CLI subcommand dispatch — `doctor`, `init`, `clone`, `sync`, `groups`
- Responsibilities: glab auth preflight, shallow-clone the curated repo set, reconcile disk against the server, optionally drive reindex + daemon reload

**`moedex-mcp` (single-repo MCP):**
- Location: `cmd/moedex-mcp/main.go`
- Triggers: an agent launching it on stdio
- Responsibilities: ingest one repo, build index + token index + symbol layer, wire the ranker, serve `search_context`

**`moedex` (one-shot CLI):**
- Location: `cmd/moedex/main.go`
- Triggers: `moedex -repo DIR [-regex] PATTERN`
- Responsibilities: index one repo in memory, print `path:line`

**`moedex-parity` (gate):**
- Location: `cmd/moedex-parity/main.go`
- Triggers: `make parity` / `make verify`
- Responsibilities: build + battery + three oracles → `PARITY-REPORT.md`, non-zero exit on failure

**`moedex-nav` (LSP arm, `-tags lsp`):**
- Location: `cmd/moedex-nav/main.go` (stub: `cmd/moedex-nav/main_disabled.go`)
- Triggers: `-verb def|refs|impl` over `FILE:LINE:COL`
- Responsibilities: drive an out-of-process language server for type-resolved navigation

**`scale` (sizing tool):**
- Location: `cmd/scale/main.go`
- Triggers: manual corpus sizing runs
- Responsibilities: index many repos, report size/throughput/mmap memory

## Architectural Constraints

- **Dependency budget.** The default build is pure Go standard library. Only
  `-tags onnx` may pull `sugarme/tokenizer` + `yalue/onnxruntime_go`; only
  `-tags moedex_simd` may use archsimd. `go.mod` direct requires are limited to
  the MCP SDK (`modelcontextprotocol/go-sdk`, `google/jsonschema-go`) plus the two
  ONNX-tagged modules.
- **Corpus isolation is enforced by a test.** `internal/ingest` must have no
  transitive production dependency on `moedex/internal/corpus`; the guard lives in
  `internal/ingest/source_test.go:28` and fails the build if the edge appears. The
  engine reaches the managed-corpus schema only through the leaf
  `internal/corpus/catalog`.
- **Never under-approximate.** Any change to `internal/query`, `internal/search`,
  or `internal/parity` must keep `make verify` green. Candidate generation may
  return extra blobs; it may never omit one ripgrep would match.
- **Byte offsets, not rune offsets.** Every offset in `index.Posting`,
  `symbol.Symbol`, `graph.Evidence`, and `diskgraph.Key` is a byte offset.
- **`Tokenize` is frozen.** `internal/tokenindex` `Tokenize` feeds BM25; changing
  it shifts ranking corpus-wide and invalidates no cache (the token index records
  no version).
- **Threading:** the process is a normal Go server — goroutine-per-request with
  bounded fan-out. Per-shard scans are capped at `NumCPU`; verify parallelism is
  gated by a package-level semaphore
  (`internal/search/search.go:59` `verifyPermits`, capacity `NumCPU-1`, engaged
  above `verifyParallelThreshold = 32`); `-http` and `/mcp` each have their own
  in-flight caps (`-search-max-concurrency`, `-mcp-max-concurrency`).
- **Global state:** three package-level mutable values only —
  `internal/search/search.go:59` (`verifyPermits` semaphore),
  `internal/setops/setops.go:33` (`intersectImpl`, assigned once in `init()` by the
  selected build tag), and `internal/blobstore/build.go:22` (`statFn`, a test seam).
  Everything else at package scope is an immutable table, compiled regexp, or
  sentinel error. Only `internal/setops` defines `init()`.
- **Circular imports:** none. The internal package graph is a DAG; `internal/server`
  is the only package that imports across every layer.
- **Off-heap invariant:** postings and blob content are served from mmap'd regions.
  `LoadMmapDeduped` hands out zero-copy sub-slices of one corpus-wide
  `blobs.dat`; nothing copies corpus bytes onto the Go heap on the serving path.
- **Format compatibility:** all binary formats are little-endian, magic-prefixed,
  and versioned (`MOEDEX03`/`MOEDEX04`/`MOEDEX05`, `MOECONT1`, `MOEBLOB1`, `TKI1`,
  `MDXE`, `SYM2` with legacy `SYM1` still readable). Directory/index files are
  written temp+rename after the data file is fsynced.
- **Host scope:** `internal/corpus` reaches exactly one host,
  `gitlab.tcdevops.com`, and never handles tokens (auth is delegated to `glab`).

## Anti-Patterns

### Importing `internal/corpus` from engine or daemon code

**What happens:** a package under `internal/` (or a default-build binary) imports
`moedex/internal/corpus` to reuse a helper, dragging `os/exec`, glab, and git
shell-out machinery into the engine.
**Why it's wrong:** it breaks the quarantine that keeps the engine pure-Go and
network-free, and `internal/ingest/source_test.go` fails the build. This was
review finding F-17.
**Do this instead:** put the shared schema in the leaf package
`internal/corpus/catalog` (zero `os/exec`, zero `Runner`) and import that;
`internal/corpus` re-exports it under its historical names.

### Per-node `Keys()` + `Edges()` for incoming graph lanes

**What happens:** answering "who calls this" by iterating `Graph.Keys()` and
calling `Edges()` per node, allocating a slice per lookup.
**Why it's wrong:** the sidecar stores only forward adjacency, so an incoming lane
is a reverse sweep. Doing it per node is a binary-search-and-allocate on what is
now a default-on annotation path.
**Do this instead:** share **one** allocation-free `diskgraph.EachEdge` sweep per
hop across every incoming lane of every block — the pattern in
`internal/graph/diskgraph/diskgraph.go:1038` and `internal/server/graphneighbors.go`.

### Adding a derived sidecar without a `.meta` validator

**What happens:** a new cached artifact is written next to the shards and reused
on the next boot because the corpus fingerprint still matches.
**Why it's wrong:** a sidecar is a cache of DERIVED output. Corpus identity alone
is insufficient — a fixed extractor keeps serving pre-fix symbols until the corpus
itself changes.
**Do this instead:** record every input that decides the artifact's content in its
`.meta` (as `symbol.ExtractorsVersion` and the embedding model + chunk geometry do),
and bump the version constant whenever the producer's output could change —
`internal/server/rankcorpus.go:607`, `internal/symbol/extract.go:35`.

### Treating a case-folded literal as a case-sensitive required byte run

**What happens:** the verify-stage literal prefilter takes an `OpLiteral` produced
by `(?i)` and requires those exact bytes on the line.
**Why it's wrong:** it silently drops other-case matches — a real
under-approximation, the exact bug class the parity gate exists to catch. This
shipped once and was found by the full-corpus harness.
**Do this instead:** expand every literal through `fold.ASCIIVariants` so the Cox
reduction and the verify prefilter agree on every rune —
`internal/fold/fold.go`, `internal/search/search.go` (`requiredLiterals`).

### Widening the tool contract instead of the tool implementation

**What happens:** a new tool argument or result field is added directly in a
handler, so `tools/list` and the runtime behavior drift apart.
**Why it's wrong:** the MCP contract is process-stable and snapshot-stamped;
agents cache it. Divergence shows up as a silently ignored argument.
**Do this instead:** declare the argument in `internal/mcp/contracts.go` (which
validates per tool name) and let the handler read it — `internal/mcp/contracts.go:206`.

### Building an index inside the daemon

**What happens:** a serving path calls `index.AddFile`, `tokenindex.Build`, or
`embed.BuildStore` inline to satisfy a request or a reload.
**Why it's wrong:** it reintroduces cold start and, for embeddings, a full corpus
re-embed on the request path. `embed.BuildStore` has no reuse path — only
`RefreshEmbeddings` does.
**Do this instead:** build offline (`moedex-index build`, `BuildSidecars`,
`moedex-serve -build-embeddings`) and hot-swap the generation on SIGHUP —
`cmd/moedex-serve/reload.go`.

## Error Handling

**Strategy:** idiomatic Go — errors are values, returned and wrapped; no panics on
the request path (5 `panic` sites total in non-test code).

**Patterns:**
- Wrap with context and preserve the chain: `fmt.Errorf("pkg: doing X: %w", err)`
  is used at 718 `fmt.Errorf` sites, 314 of which carry `%w`.
- Package-prefixed messages so a bare error string identifies its origin
  (`"snapshot: no current snapshot"`, `"navigate: language server died"`).
- Exported sentinels for conditions callers branch on: `snapshot.ErrNoCurrent`,
  `snapshot.ErrBuildLocked`, `navigate.ErrServerDead`, `navigate.ErrCooldown`,
  `navigate.ErrPrivacyRestricted`, `fmindex.ErrNULInput`.
- Unexported sentinels for capability-absent stubs: `errNoONNX`, `errNoLSP` —
  each names the build tag to rebuild with.
- **Fail closed on safety, degrade on capability.** An invalid `.ai-privacy.yml`
  aborts ingest; a corrupt content store fails the boot rather than serving wrong
  bytes (`OpenContentStoreVerified`, opt out with `MOEDEX_VERIFY_CONTENT=0`). By
  contrast a missing dense embedder or an unimplemented LSP method (`-32601`)
  degrades to an empty/lexical answer.
- **Distinguish independent failure sources.** `GraphToolset.Reload` returns
  `(openErr, closeErr)` because "the new generation failed to load" and "the swap
  succeeded but retiring the old mmaps leaked" are different operator problems
  (`internal/server/graphtools.go:440`).
- **Best-effort cache writes never fail a boot** — a failed sidecar persist is
  logged and ignored.
- Context cancellation is checked on a stride (`cancelCheckStride = 256`) inside
  scan loops so an expired HTTP request aborts promptly.

## Cross-Cutting Concerns

**Logging:** structured `log/slog` in the daemon (`cmd/moedex-serve/obs.go`), with
a `/metrics` endpoint carrying latency histograms
(`cmd/moedex-serve/obs.go:66` `defaultBuckets`). CLIs write progress and
diagnostics to stderr and results to stdout, so stdout stays pipeable. Library
packages under `internal/` do not log — they return errors or accept a `logf
func(string, ...any)` callback.

**Validation:** MCP tool arguments are validated per tool name against the
declared contract (`internal/mcp/contracts.go`), with a request-size cap
(`internal/mcp/result.go:213` `errRequestTooLarge`). Path inputs are checked for
root escape (`internal/corpus/clone.go:85` `errUnsafeDest`). Binary formats
self-validate on open: magic + version, and the content store re-hashes every
entry against its content-addressed key.

**Authentication:** the engine holds no credentials. Corpus acquisition delegates
auth entirely to `glab`; the `-http` API offers optional bearer auth
(`-auth-token` / `MOEDEX_AUTH_TOKEN`) with `/healthz` and `/metrics` left open
(`cmd/moedex-serve/middleware.go:23`), an unconditional loopback-default bind, and
optional TLS. Credentials in shell-out output are redacted before logging
(`internal/corpus/runner.go:33` `credentialRedactors`).

**Privacy:** `.ai-privacy.yml` gates ingest before file open — missing/empty means
level 3, invalid fails closed, global level 1 returns no content. The effective
policy fingerprint is recorded per repo in the freshness manifest, so a
policy-only restriction triggers a rebuild even with no commit. The LSP arm
refuses to launch a language server in a level-1 workspace
(`internal/navigate/pool.go:88`).

**Configuration:** environment-first with an explicit precedence chain in the
daemon — flag > `-config` file (systemd `EnvironmentFile` format) > process env >
built-in default (`cmd/moedex-serve/main.go:51`). Roughly 50 `MOEDEX_*` variables
exist; the load-bearing ones are `MOEDEX_SHARD_DIR`, `MOEDEX_INDEX_DIR`,
`MOEDEX_CORPUS`, `MOEDEX_HTTP_ADDR`, `MOEDEX_MCP_HTTP_ADDR`, `MOEDEX_AUTH_TOKEN`,
`MOEDEX_EMBED*`, and `MOEDEX_VERIFY_CONTENT`.

**Determinism:** ranking, context assembly, and tool output are fully ordered
(score desc, then originating order, then path, then start line) so identical
inputs produce byte-identical results — a precondition for the parity and gold
gates.

---

*Architecture analysis: 2026-08-24*
