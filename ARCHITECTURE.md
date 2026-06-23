# moedex Architecture

moedex is a clean-room, single-node trigram code-search engine and agent context
API written in Go 1.26 (`go.mod` declares `module moedex` / `go 1.26`). It keeps
the part of Zoekt that aged well — a **positional-trigram retrieval core** — and
replaces everything around it: content is **addressed by git blob SHA** (so
identical content is indexed once), trigrams are **positional byte-trigrams**
(byte offsets, not rune offsets), and a regex is reduced to a **necessary-condition
boolean trigram query** that selects candidate blobs which a real regex engine then
verifies. Correctness is pinned to **ripgrep parity** (`internal/search/parity_test.go`
shells out to `rg` and asserts identical line matches). The whole system is
**pure Go standard library** with a single *optional* external dependency: a local
HTTP embedding server for the dense-retrieval arm — absent that, moedex runs
fully self-contained.

The design lineage lives in [`zoekt-2026-redesign.md`](zoekt-2026-redesign.md)
(the northstar) and the [`research/`](research) notes; this document describes the
code as it actually exists today.

> **Note on stability.** `internal/search`, `internal/rank`, and `internal/symbol`
> are under active development. Their *exported* surface (the contracts other
> packages compile against) is described here as stable; treat those packages'
> unexported internals as potentially in flux.

---

## Package map

All library code lives under `internal/`; executables under `cmd/`.

| Package | Path | Responsibility | Key exported surface |
|---|---|---|---|
| trigram | [`internal/trigram`](internal/trigram) | The positional-trigram primitive | `const N = 3`; `type Trigram [N]byte`; `(Trigram) String()` |
| index | [`internal/index`](internal/index) | In-memory content-addressed trigram index | `Index`, `New`, `(*Index) AddFile/Postings/Blob/NumBlobs/Trigrams/Snapshot`; `Blob`, `FileRef`, `Posting`, `BlobData`; `Restore`, `RestoreLazy`, `PostingProvider`; `EncodePostings`, `DecodePostings` |
| ingest | [`internal/ingest`](internal/ingest) | Read a git repo's tracked text files; discover all repos under a root | `File`; `Repo(repoName, dir) ([]File, error)`; `DiscoverRepos(root) ([]string, error)`; `CountGitEntries(root) (int, error)` |
| query | [`internal/query`](internal/query) | Regex → boolean trigram query (Cox reduction) | `Query` (`Eval`, `String`); `All`; `And`, `Or`; `FromRegexp(pattern) (Query, error)` |
| search | [`internal/search`](internal/search) | Candidate retrieval + verify → line matches | `Match`; `Literal(ix, q) []Match`; `Regex(ix, pattern) ([]Match, error)` |
| diskstore | [`internal/diskstore`](internal/diskstore) | Persist/reload the index; mmap postings | `Save(ix, path)`; `Load(path)`; `LoadMmap(path) (*index.Index, io.Closer, error)` |
| tokenindex | [`internal/tokenindex`](internal/tokenindex) | Persistent inverted index of BM25 term stats | `TokenIndex`; `Build(ix)`; `Tokenize(text)`; `NumDocs/AvgDocLen/DocLen/DocFreq/TermFreq`; `Save`, `Load` |
| embed | [`internal/embed`](internal/embed) | Dense arm: chunk → vector → cosine search | `Vector`, `Embedder`, `HTTPEmbedder`, `NewHTTPEmbedder`; `Chunk`, `ChunkBlob`; `Store`, `BuildStore`, `Hit`, `(*Store) Search/Save`; `LoadStore` |
| rank | [`internal/rank`](internal/rank) | Fuse lexical + dense arms via RRF | `RankedResult`, `LineSpan`; `Config`; `Ranker`, `New`, `(*Ranker) Rank` |
| contextwin | [`internal/contextwin`](internal/contextwin) | Assemble ranked results into token-budgeted blocks | `ContextBlock`, `ContextWindow`, `Options`; `Assemble(ix, results, opts) ContextWindow` |
| symbol | [`internal/symbol`](internal/symbol) | Syntactic symbol layer (Go only) for block scoping | `Symbol`, `Kind`, `Index`, `NewIndex`, `Enclosing`, `EnclosingBytesFunc`; `Extractor`, `GoExtractor`, `Build`; `Save`, `Load` |
| eval | [`internal/eval`](internal/eval) | IR-metrics + ranker evaluation harness | `GoldQuery`, `NewBinaryGold`; `RecallAtK`, `PrecisionAtK`, `MRR`, `NDCGAtK`; `Runner`, `NewRunner`, `Evaluate`, `Report`, `QueryReport`; `BuildIndexFromCorpus`, `BuildIndexFromFiles` |
| parity | [`internal/parity`](internal/parity) | Full-corpus exact-match retrieval parity harness (sharded build, seeded battery, rg/gold/Zoekt oracles, adjudication, report) | `Config`, `RunConfig`, `Run`; `Build`, `Built`, `FileTable`; `Generate`, `Battery`, `Query`, `Bucket`; `QueryResult`, `Verdict`; `WriteReport`, `ReportMeta` |
| mcp | [`internal/mcp`](internal/mcp) | Serve `search_context` over MCP (JSON-RPC/stdio) | `ContextSearcher`; `Server`, `NewServer`, `Serve`; `IndexSearcher`, `NewIndexSearcher`, `SetEnclosingBytes`, `SearchContext` |
| moedex | [`cmd/moedex`](cmd/moedex) | CLI: index one repo, run a literal/regex query | — |
| moedex-mcp | [`cmd/moedex-mcp`](cmd/moedex-mcp) | MCP server binary | — |
| scale | [`cmd/scale`](cmd/scale) | Index many repos, report size/throughput/mmap memory | — |
| moedex-parity | [`cmd/moedex-parity`](cmd/moedex-parity) | Full-corpus parity gate: build + battery + oracles + `PARITY-REPORT.md`, non-zero exit on failure | — |

---

## Data flow

### Pipeline A — index build

```
git ls-files ──▶ ingest.Repo ──▶ index.AddFile (per file)
                                      │
                                      ├─▶ tokenindex.Build   (BM25 term stats)
                                      ├─▶ embed.BuildStore   (optional dense vectors)
                                      └─▶ symbol.Build       (Go symbol ranges)

index ──▶ diskstore.Save ──▶ diskstore.Load / LoadMmap
```

1. **Ingest** ([`ingest.Repo`](internal/ingest/ingest.go)) shells out to
   `git -C <dir> ls-files -s -z`, taking git's own blob SHA as the content
   identity. Files are read from the working tree; **binary blobs** (those
   containing a NUL byte, as ripgrep detects them) are skipped, and a leading
   UTF-8 BOM is stripped so line/match boundaries stay aligned with ripgrep.
2. **Index** ([`index.AddFile`](internal/index/index.go)) deduplicates by SHA: a
   repeat SHA only appends a new `FileRef` to the existing `Blob`. New content is
   copied into the blob and every positional byte-trigram is emitted as a
   `Posting{Blob, Offset}`. Because blobs are added in increasing ID order and a
   blob's trigrams in increasing offset order, each posting list stays sorted by
   `(Blob, Offset)` with no explicit sort.
3. **Sidecars** are built from the same in-memory index:
   - [`tokenindex.Build`](internal/tokenindex/tokenindex.go) tokenizes every blob
     (the frozen `Tokenize` rule: Unicode word-runs, case-split on
     camelCase/acronym boundaries, emitting both subtokens and the whole run,
     lowercased) and records document frequency, per-blob term frequency, and
     document lengths — the BM25 input set.
   - [`embed.BuildStore`](internal/embed/embed.go) (optional) chunks each blob into
     overlapping line-windows (`ChunkBlob`), embeds them in batches of 64 via the
     `Embedder`, and stores L2-normalized vectors in a flat brute-force `Store`.
   - [`symbol.Build`](internal/symbol/extract.go) runs `GoExtractor` over each
     blob; blobs that don't parse as Go simply get no symbols.
4. **Persistence** ([`diskstore.Save`](internal/diskstore/diskstore.go)) writes the
   index to a single file; `LoadMmap` maps it back with posting lists left on disk.

### Pipeline B — query / serving

```
MCP tools/call search_context
   └─▶ IndexSearcher.SearchContext           (internal/mcp/mcp.go)
          ├─▶ rank.Ranker.Rank               (internal/rank/ranker.go)
          │      ├─ lexical arm: trigram candidate blobs + BM25 (tokenindex)
          │      ├─ dense arm:   embed.Store.Search (optional)
          │      └─ fuse:        Reciprocal Rank Fusion (RRF) ──▶ []RankedResult
          └─▶ contextwin.Assemble            (internal/contextwin/contextwin.go)
                 ├─ expand each salient LineSpan to its enclosing block
                 │    (symbol.Index.Enclosing when wired, else brace/indent heuristic)
                 ├─ merge overlapping/adjacent blocks per file
                 └─ emit best-first under a token budget ──▶ ContextWindow
```

The call chain is wired in [`cmd/moedex-mcp/main.go`](cmd/moedex-mcp/main.go):
it builds the index + token index, conditionally builds the dense store, constructs
a `rank.Ranker`, wraps it in an `mcp.IndexSearcher` (default `topK = 20`), wires the
Go symbol layer in via `searcher.SetEnclosingBytes(symIdx.EnclosingBytesFunc())`,
and serves on stdin/stdout.

Inside [`Ranker.Rank`](internal/rank/ranker.go): the **lexical arm** generates
candidate blobs by unioning the trigram candidate sets of each query term (terms
shorter than 3 bytes are skipped for candidate generation but still score), then
BM25-scores each candidate (`K1`=1.2, `B`=0.75 by default). The **dense arm**
(only when both `store` and `emb` are non-nil) does cosine search over chunk
embeddings, keeping the best chunk per blob. The two ranked lists are fused by
**RRF**: each arm contributes `1/(RRFk + rank)` at a blob's rank (`RRFk`=60
default). Results carry both the fused score and the salient `LineSpan`s that seed
context extraction.

[`contextwin.Assemble`](internal/contextwin/contextwin.go) expands each span into a
block, merges overlapping/touching blocks within a file, walks results best-first
(score desc, then originating order, then path, then start line — fully
deterministic), and emits blocks while the running token estimate
(`ceil(len(text)/4)`) stays within budget (`DefaultTokenBudget = 8000`). The first
block is always emitted even if it alone exceeds budget; any later skip sets
`Truncated`. The MCP layer renders the window as text under
`path:start-end (score)` headers.

---

## Key design decisions & rationale

- **Byte offsets, not rune offsets.** Zoekt used rune offsets;
  [`internal/trigram`](internal/trigram/trigram.go) documents the clean-room choice
  to use bytes: content stays 1× in memory, the positional-distance delta for a
  literal is a fixed byte length, and candidate verification is byte-exact like
  ripgrep — correct over UTF-8 regardless of where trigrams straddle rune
  boundaries.
- **Content dedup by git blob SHA.** `index.AddFile` indexes identical content once
  and accumulates `FileRef`s, so a blob can back several paths. This is the
  Blackbird content-addressable instinct from the northstar; distribution/sharding
  remains a future seam the design leaves room for but does not yet pay for.
- **mmap'd compact postings.** Posting lists are the memory wall (~16 bytes each,
  ~one per byte of corpus). [`index/codec.go`](internal/index/codec.go) compresses
  them with **grouped varint delta coding** (per blob: blob-id delta, count, then
  offset deltas), typically 1–2 bytes per posting. `diskstore.LoadMmap` maps the
  whole file and hands the index self-contained byte sub-slices per trigram, so a
  query decodes only the trigrams it touches and the bulk of postings never enter
  the Go heap. `cmd/scale` measures this directly (`MOEDEX_MMAP=1`).
- **Optional dense arm (zero-dep default).** The dense arm lives behind the
  `Embedder` interface and is `nil`-able; `rank.New(ix, ti, nil, nil, cfg)` yields
  pure-lexical ranking with no external dependencies. A local HTTP embedding server
  lights up the hybrid when configured. Vectors are L2-normalized so cosine reduces
  to a dot product over a flat (brute-force) store — an ANN index is a later
  optimization behind the same `Search` API.
- **RRF fusion (not a learned reranker).** Reciprocal Rank Fusion needs no score
  calibration between the BM25 and cosine arms and is the northstar's conservative
  default; a learned reranker is explicitly deferred (see
  [`research/learned-reranker.md`](research/learned-reranker.md)).
- **Necessary-condition query reduction.** [`internal/query`](internal/query/query.go)
  guarantees ripgrep parity by only ever producing a *necessary* condition for a
  match (it may over-approximate, never under-approximate); when unsure any node
  degrades to `All` (scan everything). `FromRegexp` uses the full Cox-style
  bottom-up analysis ([`cox.go`](internal/query/cox.go)) with per-node
  exact/prefix/suffix sets (capped at 8) and boundary-trigram synthesis across
  concatenation. `search.Regex` selects candidates with this query, then verifies
  each candidate line with Go's real `regexp` engine.

---

## On-disk formats

All four sidecar/store formats are little-endian and round-trippable.

| Format | Magic | Writer | Layout |
|---|---|---|---|
| Trigram index | `MOEDEX03` (v3) | [`diskstore`](internal/diskstore/diskstore.go) | 48-byte header (magic, version, reserved, numBlobs, numTrigrams, blobOff, postOff), then a **blob section** (per blob: SHA, content, file refs) and a **postings section** (per trigram: 3 bytes + uint64 encLen + varint-delta encoded list). Each list is an individually-addressable byte range so `LoadMmap` can hand out sub-slices. |
| Token index | `TKI1` (v1) | [`tokenindex/codec.go`](internal/tokenindex/codec.go) | 4-byte magic + version, then the BM25 term statistics; `Save`/`Load` round-trip them. |
| Embedding store | `MDXE` (v1) | [`embed/codec.go`](internal/embed/codec.go) | 4-byte magic, version, dim, count; then `count` chunk records (blob, startLine/endLine as uint32, startByte/endByte as uint64); then `count` contiguous float32 vectors. |
| Symbol sidecar | `SYM1` | [`symbol/codec.go`](internal/symbol/codec.go) | 4-byte magic, uvarint blob count; per blob: blobID, symCount, then per symbol the name, kind, and four byte offsets (nameStart/nameEnd/bodyStart/bodyEnd) — all uvarint. |

---

## Full-corpus parity harness (`internal/parity`)

The retrieval correctness invariant — **moedex never under-approximates** (it
returns every line ripgrep does) and returns no spurious lines — is scaled from
the small `internal/search` parity test to the entire `~/TCGitlab` corpus by
[`internal/parity`](internal/parity), driven by
[`cmd/moedex-parity`](cmd/moedex-parity) and gated by `make verify` / `make parity`.

- **Sharded build.** Building the whole corpus in one in-RAM index would blow the
  posting-map memory wall, so `parity.Build` ingests repos into a sequence of
  on-disk shards (one ~150–250MB-content shard built in RAM at a time, saved via
  `diskstore`, then freed). It also records the indexed file set `F` (a
  `FileTable` mapping each file to a stable integer id), materializes each file's
  *indexed* bytes into a flat scratch **content mirror**, and samples corpus
  terms for the battery — all in one pass. The corpus is read-only.
- **Scope pinning (`F`).** Every oracle searches the mirror, so ripgrep's and
  Zoekt's scanned-file set *is* `F`, byte-for-byte (BOM already stripped, no
  `.gitignore`/binary-detection ambiguity). Matches are compared at `(file, line)`
  granularity; byte-spans are intentionally not used because RE2 (leftmost-longest)
  and Rust-regex submatch boundaries differ and would flag engine quirks, not
  retrieval errors.
- **Seeded battery.** `parity.Generate` builds a deterministic ≥1000-query battery
  spanning nine buckets (common/rare/phrase/metachar literals, sub-trigram,
  high-frequency, regex, case-insensitive, unicode). Each `Query` carries
  `Literal`/`IgnoreCase`/`NoUnicode` flags chosen so ripgrep, the gold scan, and
  moedex run with identical matching semantics (e.g. `--no-unicode` aligns rg's
  `\w\d\s\b` with Go RE2's ASCII shorthand; case-insensitive stays in Unicode mode
  so rg `-i` folding matches Go `(?i)`).
- **Three oracles + adjudication.** ripgrep is the immovable ground truth. An
  independent in-process Go-regexp/`bytes.Contains` scan over every blob (the
  **gold** oracle, using no trigram filtering) classifies any moedex-vs-ripgrep
  divergence: moedex's candidate blobs are a subset of gold's and both verify with
  the same Go engine, so `moedex ⊆ gold` always — a `gold \ moedex` non-empty set
  is a **real under-approximation** (the bug class AC-D3 targets), while a
  moedex-vs-ripgrep difference with `moedex == gold` is a documented RE2-vs-Rust
  semantics quirk. Zoekt is a soft competitive differential (file granularity):
  it must never beat moedex on truth-recall.

This harness surfaced and fixed a real under-approximation: the `internal/search`
verify-stage literal prefilter (`requiredRun`) treated a case-folded `OpLiteral`
(from `(?i)`) as a case-*sensitive* required byte run, dropping other-case matches.

## Current state & deliberately deferred

**Built and working today:**

- In-memory positional byte-trigram index with SHA content dedup.
- Full Cox-style regex → boolean trigram reduction, verified to ripgrep parity.
- On-disk persistence (`MOEDEX03`) with mmap'd compact varint-delta postings.
- BM25 lexical ranking over a persistent token index.
- Optional dense arm (local embedding server) fused with lexical via RRF.
- Token-budgeted, deduplicated, block-scoped context-window assembly.
- A Go-only syntactic symbol layer (`go/parser`) that scopes context blocks to
  real definition boundaries.
- MCP `search_context` tool over stdio JSON-RPC.
- An IR-metrics evaluation harness (recall@k, precision@k, MRR, nDCG@k).
- A full-corpus exact-match retrieval parity harness (`internal/parity`,
  `cmd/moedex-parity`): sharded whole-corpus build, seeded ≥1000-query battery,
  ripgrep ground truth + independent gold adjudicator + Zoekt differential,
  gated by `make verify` and reported in `PARITY-REPORT.md`.

**Deliberately deferred (design intentions, not yet built)** — tracked in the
northstar and [`research/`](research):

- **Incremental / delta indexing** — the data model is content-addressed to keep
  this first-class, but no incremental update path exists yet.
- **Distribution / sharding** — single-node only; blob-SHA addressing leaves room
  for it.
- **Native SIMD intersection/verification kernel** — see
  [`research/simd-kernel.md`](research/simd-kernel.md); the engine is pure Go.
- **FM-index compressed cold tier** — see
  [`research/fm-index-cold-tier.md`](research/fm-index-cold-tier.md).
- **Learned reranker** — RRF is the current fusion; see
  [`research/learned-reranker.md`](research/learned-reranker.md).
- **Multi-language symbols** — only `GoExtractor` exists; tree-sitter/SCIP for other
  languages is deferred behind the `Extractor` interface (see
  [`research/symbol-layer.md`](research/symbol-layer.md)).
- **Find-refs / go-to-def, symbol-name ranking arm, an ANN vector index, and
  agent-consumer metrics (UDCG)** are all noted as future work.

---

## Build & run

```sh
go build ./...        # build all packages and commands
go test ./...         # run the test suite (parity tests shell out to `rg`)
make verify           # master gate: health + round-trip + full-corpus parity
make parity           # just the full-corpus parity gate -> PARITY-REPORT.md
make setup            # one-time: install the Zoekt differential oracle
```

The parity tests in `internal/search` and the `internal/parity` harness require
the `rg` (ripgrep) binary on `PATH`; the Zoekt differential additionally needs
`zoekt-index`/`zoekt` (install via `make setup`, skipped gracefully if absent).
`make verify`/`make parity` index the corpus at `MOEDEX_CORPUS` (default
`~/TCGitlab`).

### `moedex` — one-shot search CLI

```sh
moedex -repo DIR [-regex] PATTERN
```

| Flag | Default | Meaning |
|---|---|---|
| `-repo` | `.` | path to a git repo to index |
| `-regex` | `false` | treat `PATTERN` as a regular expression instead of a literal |

Indexes the repo in memory and prints `relpath:line` for each matching line.

### `moedex-mcp` — MCP server

```sh
moedex-mcp -repo DIR
```

| Flag | Default | Meaning |
|---|---|---|
| `-repo` | `.` | path to a git repo to index |
| `-chunk-lines` | `40` | lines per embedding chunk (dense arm) |
| `-chunk-overlap` | `10` | overlapping lines between chunks (dense arm) |

The server ingests the repo, builds the trigram + token indexes and the Go symbol
layer, optionally builds the dense store, and serves the `search_context` tool
(args: `query` required, `token_budget` and `top_k` optional) over stdin/stdout.

**Dense arm (optional) — environment variables:**

| Variable | Effect |
|---|---|
| `MOEDEX_EMBED_URL` | Base URL of a local OpenAI/ollama-style embeddings endpoint (e.g. `http://localhost:11434/v1`). Setting it enables the dense arm; `embed.Embed` POSTs to `{URL}/embeddings`. |
| `MOEDEX_EMBED_MODEL` | Model name sent in the embedding request body. |

Without `MOEDEX_EMBED_URL` the server logs that the dense arm is disabled and runs
pure-lexical with zero external dependencies.

### `scale` — corpus sizing tool

```sh
scale ROOT [sampleRegex]          # MOEDEX_MMAP=1 to also measure mmap-loaded heap
```

Walks `ROOT` for git repos, indexes them all into one in-memory index, and reports
file/blob counts, dedup ratio, trigram/posting counts, build throughput, and heap
usage as a multiple of content size. With `MOEDEX_MMAP=1` it persists the index,
drops the in-RAM copy, reloads with postings mmap'd, and reports the heap
difference.
