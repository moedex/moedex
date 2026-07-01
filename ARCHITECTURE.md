# moedex Architecture

moedex is a clean-room, single-node trigram code-search engine and agent context
API written in Go 1.26 (`go.mod` declares `module moedex` / `go 1.26`). It keeps
the part of Zoekt that aged well — a **positional-trigram retrieval core** — and
replaces everything around it: content is **addressed by git blob SHA** (so
identical content is indexed once), trigrams are **positional byte-trigrams**
(byte offsets, not rune offsets), and a regex is reduced to a **necessary-condition
boolean trigram query** that selects candidate blobs which a real regex engine then
verifies. Correctness is pinned to **ripgrep parity** (`internal/search/parity_test.go`
shells out to `rg` and asserts identical line matches). The **default build is pure
Go standard library** with zero ML/runtime dependencies: the dense-retrieval arm is
optional and absent it, moedex runs fully self-contained. The dense arm has two
backends — an **in-process ONNX code embedder** compiled in only under the `onnx`
build tag (which is the only thing that pulls the `github.com/sugarme/tokenizer` and
`github.com/yalue/onnxruntime_go` modules in `go.mod`), or a **local HTTP embedding
server**. Neither is needed for lexical/symbol/path retrieval.

The design lineage lives in [`zoekt-2026-redesign.md`](zoekt-2026-redesign.md)
(the northstar) and the [`research/`](research) notes; the **architectural
decisions — with their evidence — are recorded as ADRs in [`docs/adr/`](docs/adr)**.
This document describes the code as it actually exists today.

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
| fold | [`internal/fold`](internal/fold) | ASCII case-fold variants for a rune, shared by `query` (Cox reduction) and `search` (verify prefilter) so both agree on every rune | `ASCIIVariants(r) (bytes []byte, allASCII bool)` |
| index | [`internal/index`](internal/index) | In-memory content-addressed trigram index | `Index`, `New`, `(*Index) AddFile/Postings/Blob/NumBlobs/Trigrams/Snapshot`; `Blob`, `FileRef`, `Posting`, `BlobData`; `Restore`, `RestoreLazy`, `PostingProvider`; `EncodePostings`, `DecodePostings` |
| ingest | [`internal/ingest`](internal/ingest) | Read a git repo's tracked text files; discover all repos under a root | `File`; `Repo(repoName, dir) ([]File, error)`; `DiscoverRepos(root) ([]string, error)`; `CountGitEntries(root) (int, error)` |
| query | [`internal/query`](internal/query) | Regex → boolean trigram query (Cox reduction) | `Query` (`Eval`, `String`); `All`; `And`, `Or`; `FromRegexp(pattern) (Query, error)` |
| search | [`internal/search`](internal/search) | Candidate retrieval + verify → line matches | `Match`; `Literal(ctx, ix, q) ([]Match, error)`; `Regex(ctx, ix, pattern) ([]Match, error)` |
| diskstore | [`internal/diskstore`](internal/diskstore) | Persist/reload the index; mmap postings; the deduped served format + shared content store | `Save(ix, path)`; `Load(path)`; `LoadMmap(path) (*index.Index, io.Closer, error)`; `LoadBlobs`; `SaveDeduped`, `LoadMmapDeduped`, `LoadBlobsDeduped`, `IsDeduped`; `ContentStoreWriter`, `NewContentStoreWriter`, `ContentStore`, `OpenContentStore`, `ContentStoreName` |
| blobstore | [`internal/blobstore`](internal/blobstore) | Global content-addressable store (CAS): each unique blob stored once corpus-wide (cross-shard dedup) + per-blob delta refresh + deduped served export | `Store`, `Open`, `(*Store) Has/Put/Get/Len/BytesStored/SHAs/Close`; `BlobManifest`, `RepoBlobs`, `Stats`, `WriteBlobManifest`, `LoadBlobManifest`; `BuildCAS`, `RefreshCAS`, `DeltaStats`, `ExportShardDir`, `ExportDedupedShardDir` |
| tokenindex | [`internal/tokenindex`](internal/tokenindex) | Persistent inverted index of BM25 term stats | `TokenIndex`; `Build(ix)`; `Tokenize(text)`; `NumDocs/AvgDocLen/DocLen/DocFreq/TermFreq`; `Save`, `Load` |
| embed | [`internal/embed`](internal/embed) | Dense arm: chunk → vector → cosine search | `Vector`, `Embedder`; `HTTPEmbedder`, `NewHTTPEmbedder`; `ONNXEmbedder`, `NewONNXEmbedder`, `NewONNXEmbedderFromFiles` (real only under `-tags onnx`; a no-op stub otherwise); `Chunk`, `ChunkBlob`; `Store`, `BuildStore`, `Hit`, `(*Store) Search/Save/Len/Dim`; `LoadStore` |
| rank | [`internal/rank`](internal/rank) | Fuse lexical + dense + symbol + path arms via RRF | `RankedResult`, `LineSpan`; `Config`; `Ranker`, `New`, `(*Ranker) Rank/SetSymbols/SetDense/UseTokenCandidates` |
| contextwin | [`internal/contextwin`](internal/contextwin) | Assemble ranked results into token-budgeted blocks | `ContextBlock`, `ContextWindow`, `Options`; `Assemble(ix, results, opts) ContextWindow` |
| symbol | [`internal/symbol`](internal/symbol) | Polyglot syntactic symbol layer for block scoping + the symbol-name arm | `Symbol`, `Kind`, `Role`, `Occurrence`, `Ref`, `Index`, `NewIndex`, `Enclosing`, `EnclosingBytesFunc`, `(*Index) References/Definitions`; `Extractor`, `GoExtractor`, `CSharpExtractor`, `TSExtractor`, `SQLExtractor`, `CFExtractor`, `ExtractorForPath`, `Build`, `BuildMulti`; `Save`, `Load` |
| eval | [`internal/eval`](internal/eval) | IR-metrics + ranker evaluation harness | `GoldQuery`, `NewBinaryGold`; `RecallAtK`, `PrecisionAtK`, `MRR`, `NDCGAtK`; `Runner`, `NewRunner`, `Evaluate`, `Report`, `QueryReport`; `BuildIndexFromCorpus`, `BuildIndexFromFiles` |
| parity | [`internal/parity`](internal/parity) | Full-corpus exact-match retrieval parity harness + shard-level freshness | `Config`, `RunConfig`, `Run`; `Build`, `Built`, `FileTable`, `DefaultShardBytes`; `Generate`, `Battery`, `Query`, `Bucket`; `QueryResult`, `Verdict`; `WriteReport`, `ReportMeta`; `Manifest`, `ShardManifest`, `RepoHead`, `WriteManifest`, `LoadManifest`, `DetectChanges`, `Changes`, `Rebuild` |
| server | [`internal/server`](internal/server) | Warm multi-shard serving spine: mmap'd retrieval + ranked agent context | `Corpus`, `Open`, `(*Corpus) Regex/Literal/NumShards/NumBlobs/Close`; `RankCorpus`, `RankConfig`, `OpenRank`, `(*RankCorpus) SearchContext`; `BuildSidecars` |
| mcp | [`internal/mcp`](internal/mcp) | Serve `search_context` over MCP (JSON-RPC/stdio) | `ContextSearcher`; `Server`, `NewServer`, `Serve`; `IndexSearcher`, `NewIndexSearcher`, `SetEnclosingBytes`, `SearchContext` |
| corpus | [`internal/corpus`](internal/corpus) | Corpus acquisition + freshness over glab/git (the only package that shells out to them; **not imported by the engine**) | `Runner`, `ExecRunner`; `Config`, `DefaultGroups`; `Project`, `Enumerate`; `Doctor`, `Report`; `CloneArgs`, `CloneProjects`; `Reconcile`, `PlanSync`, `SyncProjects`; `Reindex` |
| navigate | [`internal/navigate`](internal/navigate) | Experimental LSP-precise navigation arm (ADR 0017, `-tags lsp`): type-resolved go-to-def / find-refs / find-impls via an out-of-process language server over a hand-written stdlib JSON-RPC client; multi-language registry sized to the real corpus (csharp ~60% via `csharp-ls`; typescript/js; css/scss via vscode-css-language-server; cfml via `cflsp`; html; sql; go; python; ready-but-unused rust/cpp) — partial-capability servers degrade gracefully (a `-32601` unimplemented method → empty, not error), C#'s `DOTNET_ROOT` is resolved per-launch via `LangSpec.ResolveEnv`, and a shared per-(root,language) server pool with idle-TTL eviction + restart backoff, incremental `didChange` sync, and live-buffer overlays; no new go.mod dep (mirrors the dense arm's build-tag boundary). ADR 0018 adds name-based navigation on top: `workspace/symbol` (root-routed, merges every already-live language server for a polyglot root when no language is pinned) and `textDocument/documentSymbol` (file-routed, flattens the hierarchical `DocumentSymbol` shape and prefers `selectionRange` over the whole declaration range) — both return the named `Symbol` type, not a bare `Location` | `Pos`, `Location`, `Symbol`, `Navigator`; `Config`; `LSP`, `NewLSP`, `(*LSP) Definition/References/Implementations/WorkspaceSymbol/DocumentSymbol/SetOverlay/DropOverlay/NotifyChanged/Alive/Close`; `Pool`, `NewPool`, `(*Pool) Navigator/NavigatorFor/Definition/References/Implementations/WorkspaceSymbol/DocumentSymbol/SetOverlay/DropOverlay/NotifyChanged/Stats/Sweep/Close`; `Stats`; `LangSpec`, `LanguageForPath`, `SpecForLanguage`, `SpecForPath`; `ErrServerDead`; `const LSPCompiled` |
| moedex | [`cmd/moedex`](cmd/moedex) | CLI: index one repo, run a literal/regex query | — |
| moedex-mcp | [`cmd/moedex-mcp`](cmd/moedex-mcp) | Single-repo MCP server binary | — |
| moedex-serve | [`cmd/moedex-serve`](cmd/moedex-serve) | Warm retrieval daemon over a prebuilt shard dir: `-http` retrieval API, `-q` one-shot, `-mcp` ranked context | — |
| moedex-index | [`cmd/moedex-index`](cmd/moedex-index) | Offline shard-dir builder/freshness tool: `build` / `check` / `refresh`, plus the CAS commands `cas-build` / `cas-refresh` / `cas-export` / `cas-compact` | — |
| scale | [`cmd/scale`](cmd/scale) | Index many repos, report size/throughput/mmap memory | — |
| moedex-parity | [`cmd/moedex-parity`](cmd/moedex-parity) | Full-corpus parity gate: build + battery + oracles + `PARITY-REPORT.md`, non-zero exit on failure | — |
| moedex-corpus | [`cmd/moedex-corpus`](cmd/moedex-corpus) | Corpus setup + freshness CLI: `doctor` / `clone` / `sync` (`-reindex`) / `groups`, scoped to gitlab.tcdevops.com | — |
| moedex-nav | [`cmd/moedex-nav`](cmd/moedex-nav) | CLI for the experimental LSP-precise navigation arm (built only with `-tags lsp`): `-verb def\|refs\|impl` over `FILE:LINE:COL`, `-lang`/`-server` selection, `-overlay` (unsaved-buffer nav), `-notify` (disk-edit invalidation), `-json` output, `-stats` (Pool counters). External language server on PATH; not in the default build. | — |

---

## Data flow

### Pipeline A — index build

```
git ls-files ──▶ ingest.Repo ──▶ index.AddFile (per file)
                                      │
                                      ├─▶ tokenindex.Build   (BM25 term stats)
                                      ├─▶ embed.BuildStore   (optional dense vectors)
                                      └─▶ symbol.BuildMulti  (polyglot symbol ranges)

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
     `Embedder` (the in-process ONNX code embedder or an HTTP server), and stores
     L2-normalized vectors in a flat brute-force `Store`.
   - [`symbol.BuildMulti`](internal/symbol/build_multi.go) dispatches a
     language-appropriate `Extractor` per blob via `ExtractorForPath`, keyed on the
     first file's extension (`.go`→`GoExtractor`, `.cs`→`CSharpExtractor`,
     `.ts`/`.tsx`→`TSExtractor`, `.sql`→`SQLExtractor`, `.cfm`/`.cfc`→`CFExtractor`);
     a blob whose extension isn't recognized, or that yields no symbols, is skipped
     and falls back to the brace/indent heuristic at scoping time.
4. **Persistence** ([`diskstore.Save`](internal/diskstore/diskstore.go)) writes the
   index to a single file; `LoadMmap` maps it back with posting lists left on disk.

### Pipeline B — query / serving

```
MCP tools/call search_context
   └─▶ IndexSearcher.SearchContext           (internal/mcp/mcp.go)
          ├─▶ rank.Ranker.Rank               (internal/rank/ranker.go)
          │      ├─ lexical arm: trigram candidate blobs + BM25 (tokenindex)
          │      ├─ path arm:    file-path token coverage (PathMinCoverage, on by default)
          │      ├─ symbol arm:  symbol-name coverage (SymbolMinCoverage; needs symbol index)
          │      ├─ dense arm:   embed.Store.Search (optional; query-length gated)
          │      └─ fuse:        Reciprocal Rank Fusion (RRF) ──▶ []RankedResult
          └─▶ contextwin.Assemble            (internal/contextwin/contextwin.go)
                 ├─ expand each salient LineSpan to its enclosing block
                 │    (symbol.Index.Enclosing when wired, else brace/indent heuristic)
                 ├─ merge overlapping/adjacent blocks per file
                 └─ emit best-first under a token budget ──▶ ContextWindow
```

The single-repo call chain is wired in
[`cmd/moedex-mcp/main.go`](cmd/moedex-mcp/main.go): it builds the index + token
index, conditionally builds the dense store, constructs a `rank.Ranker`, wraps it in
an `mcp.IndexSearcher` (default `topK = 20`), builds the polyglot symbol layer
(`symbol.BuildMulti`) and wires it both for scoping
(`searcher.SetEnclosingBytes(symIdx.EnclosingBytesFunc())`) and as a ranking arm
(`ranker.SetSymbols(symIdx)`), and serves on stdin/stdout. The warm multi-shard
equivalent is `server.OpenRank` (see [Serving spine](#warm-serving-spine-internalserver--cmdmoedex-serve)).

Inside [`Ranker.Rank`](internal/rank/ranker.go) up to four RRF arms run, each
contributing `1/(RRFk + rank)` at a blob's rank (`RRFk`=60 default):

- **Lexical arm** — generates candidate blobs by unioning the trigram candidate
  sets of each query term (terms shorter than 3 bytes are skipped for candidate
  generation but still score), then BM25-scores each candidate (`K1`=1.2, `B`=0.75
  by default). The corpus ranker switches candidate generation to the token index
  via `UseTokenCandidates(true)` because its content-only index carries no postings.
- **Path arm** (`pathArm`) — the zoekt-style filename signal. A blob whose file
  path tokens cover at least `PathMinCoverage` (default **0.6**) of the query's
  distinct terms casts a vote, so a query like "federated server" finds
  `mysql_create_federated_server.sql` even when "federated" lives only in the
  filename. It is **on by default** (every blob has a path, so it needs no external
  index; a negative `PathMinCoverage` disables it and skips building the path index).
- **Symbol arm** (`symbolArm`) — active only when a symbol index is installed
  (`SetSymbols`). A blob whose defined symbol *names* cover at least
  `SymbolMinCoverage` (default **0.67**) of the query's distinct terms votes, so a
  blob that defines `func Refund` outranks one that merely mentions the word.
- **Dense arm** (`denseArm`) — active only when both `store` and `emb` are non-nil
  AND the query passes the **query-length gate** `DenseMinQueryTerms` (default
  **5** distinct terms): short keyword queries are the lexical/symbol/path arms' home
  turf where low-cosine chunks are noise, so dense is reserved for longer
  natural-language queries. It does cosine search over chunk embeddings, keeping the
  best chunk per blob. A second, opt-in `DenseMinScore` cosine threshold (off by
  default) can additionally gate individual chunks by similarity.

Results carry the fused score, the raw BM25/cosine components, and the salient
`LineSpan`s (lexical match lines, plus each voting arm's span) that seed context
extraction.

[`contextwin.Assemble`](internal/contextwin/contextwin.go) expands each span into a
block, merges overlapping/touching blocks within a file, walks results best-first
(score desc, then originating order, then path, then start line — fully
deterministic), and packs blocks under the token budget
(`ceil(len(text)/4)` per block, `DefaultTokenBudget = 8000`). The first block is
always emitted even if it alone exceeds budget; after that, a block that would
push the running total over budget is skipped (not a break) so a later, smaller,
lower-scored block still gets a chance to fit in the remainder — maximizing
budget use rather than emitting a strict score-prefix. Any skip sets `Truncated`.
The MCP layer renders the window as text under `path:start-end (score)` headers.

---

## Key design decisions & rationale

The *why* behind each decision — with its supporting evidence — is recorded in the
ADRs under [`docs/adr/`](docs/adr). This table maps the load-bearing ones to the
code that embodies them; read the ADR for the alternatives weighed and the numbers.

| Decision | Embodied in | ADR |
|---|---|---|
| Keep the positional-trigram core; **byte** offsets, not rune offsets | `internal/trigram`, `internal/index` | [0002](docs/adr/0002-positional-trigram-core-byte-offsets.md) |
| Necessary-condition regex→trigram (Cox) reduction; ripgrep parity, never under-approximate | `internal/query`, `internal/search`, `internal/parity` | [0003](docs/adr/0003-cox-reduction-ripgrep-parity.md) |
| Content addressing by git blob SHA — global dedup, per-blob delta, deduped served format | `internal/index`, `internal/blobstore`, `internal/diskstore` | [0004](docs/adr/0004-content-addressable-blob-store.md) |
| mmap'd compact (varint-delta) postings — postings off-heap | `internal/index/codec.go`, `internal/diskstore` | [0005](docs/adr/0005-mmap-compact-postings.md) |
| Multi-arm hybrid ranking fused via RRF by default; an optional post-fusion linear-reranker mode exists but is off by default | `internal/rank` | [0006](docs/adr/0006-rrf-hybrid-ranking.md) |
| Optional dense arm — zero-dep default (ONNX behind `-tags onnx` / local HTTP) | `internal/embed` | [0007](docs/adr/0007-optional-dense-arm.md) |
| Polyglot symbol layer as a precomputed sidecar (not tree-sitter in-binary) | `internal/symbol` | [0008](docs/adr/0008-polyglot-symbol-sidecar.md) |
| Agent-first context API — token-budgeted, deduped, symbol-scoped windows over MCP | `internal/contextwin`, `internal/mcp` | [0009](docs/adr/0009-agent-context-api.md) |
| Pure-Go execution; native SIMD kernel deferred | `internal/setops`, `simd/archsimd` | [0013](docs/adr/0013-pure-go-defer-simd.md) |

The full index — including scope, the serving spine, freshness, the search-latency
work, and the evaluation gate — is in [`docs/adr/README.md`](docs/adr/README.md).

---

## On-disk formats

All binary sidecar/store formats are little-endian and round-trippable.

| Format | Magic | Writer | Layout |
|---|---|---|---|
| Trigram index | `MOEDEX03` (v3) | [`diskstore`](internal/diskstore/diskstore.go) | 48-byte header (magic, version, reserved, numBlobs, numTrigrams, blobOff, postOff), then a **blob section** (per blob: SHA, content, file refs) and a **postings section** (per trigram: 3 bytes + uint64 encLen + varint-delta encoded list). Each list is an individually-addressable byte range so `LoadMmap` can hand out sub-slices. The direct `moedex-index build` path writes this (or `MOEDEX04` for a selective build). |
| Deduped trigram index | `MOEDEX05` (v5) | [`diskstore/dedupstore.go`](internal/diskstore/dedupstore.go) | The **served-shard-dedup** format. Same 48-byte header shape as `MOEDEX03`, but the **blob section is content-less** (per blob: SHA + file refs only — no inlined content); the postings section is identical. Blob content lives once in the shared content store (below) and is resolved by SHA at load. `LoadMmapDeduped`/`LoadBlobsDeduped` take a `*ContentStore` and serve each blob's content as a zero-copy mmap sub-slice — so a blob whose repos span several shards is stored once for the whole served corpus, and content stays off the Go heap. Written by `blobstore.ExportDedupedShardDir` (`cas-export -deduped`). |
| Shared content store | `MOECONT1` (v1) | [`diskstore/contentstore.go`](internal/diskstore/contentstore.go) | The single `blobs.dat` backing a deduped (`MOEDEX05`) shard dir. 32-byte header (magic, version, reserved, numBlobs, dirOff), a **content section** (each unique blob's raw bytes, contiguous & unframed so a loader hands out zero-copy mmap sub-slices), then a **directory section** (per blob: sha, absolute contentOff, contentLen). `PutContent` is idempotent on the content hash (cross-shard dedup); the file is written atomically (temp+rename). The SHA is an opaque variable-length key, matching the CAS. **Self-verifying:** because the key is the content hash, the serving path (`OpenContentStoreVerified`) re-hashes every entry against its key at open, so a corrupt store fails the boot rather than silently serving wrong content (default-on; opt out with `MOEDEX_VERIFY_CONTENT=0`). |
| CAS blob store | `MOEBLOB1` (v1) | [`blobstore`](internal/blobstore/blobstore.go) | Two files. `blobs.pack`: append-only, one record per **unique** blob (`shaLen`+sha, `contentLen`+content) — each unique content stored once for the whole corpus. `blobs.idx`: 32-byte header (magic, version, reserved, numBlobs, packBytes) then per blob a directory entry (sha, packOff, packLen, contentLen), written atomically (temp+rename) only after the pack is fsynced, so a crash never indexes non-durable bytes. The SHA is an opaque variable-length key (SHA-1 today, SHA-256-ready). |
| Token index | `TKI1` (v1) | [`tokenindex/codec.go`](internal/tokenindex/codec.go) | 4-byte magic + version, then the BM25 term statistics; `Save`/`Load` round-trip them. |
| Embedding store | `MDXE` (v1) | [`embed/codec.go`](internal/embed/codec.go) | 4-byte magic, version, dim, count; then `count` chunk records (blob, startLine/endLine as uint32, startByte/endByte as uint64); then `count` contiguous float32 vectors. |
| Symbol sidecar | `SYM2` (legacy `SYM1` still readable) | [`symbol/codec.go`](internal/symbol/codec.go) | 4-byte magic, uvarint blob count; per blob: blobID, symCount, then per symbol the name, kind, and four byte offsets (nameStart/nameEnd/bodyStart/bodyEnd) — all uvarint. A second section (absent under the legacy `SYM1` magic) records per-blob reference occurrences (name, kind, role, start, end) so `References`/`Definitions` round-trip. Holds symbols from every language extractor (Go/C#/TS/SQL/CFML), not just Go. |

The serving layer adds two JSON sidecars that are not part of the index codecs: a
freshness `manifest.json` (`internal/parity/manifest.go` — repo→shard membership +
each repo's git HEAD) and per-cache `.meta` validators next to the corpus token,
symbol, and embedding sidecars (`internal/server/rankcorpus.go` — a shard-set
fingerprint + blob count, plus the embedding model for the embedding store, so a
stale cache is detected and rebuilt rather than silently reused). The CAS adds a
third JSON sidecar, `blobmanifest.json` (`internal/blobstore/manifest.go` —
repo→{git HEAD, ordered file entries of `{sha, rel}`} plus global dedup stats),
which records a repo's *blob set* so a per-repo refresh is a pure set-diff.

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
verify-stage literal prefilter (`requiredLiterals`) treated a case-folded `OpLiteral`
(from `(?i)`) as a case-*sensitive* required byte run, dropping other-case matches.

## Warm serving spine (`internal/server` + `cmd/moedex-serve`)

The one-shot CLIs rebuild an index per invocation. The serving spine instead reads a
**prebuilt shard directory** and answers queries with zero cold-start. The offline
side ([`cmd/moedex-index`](cmd/moedex-index)) produces and refreshes that directory;
the daemon ([`cmd/moedex-serve`](cmd/moedex-serve)) only ever reads it.

- **Retrieval corpus** ([`server.Corpus`](internal/server/corpus.go)). `Open` mmaps
  every `*.idx` shard once and holds the mappings for its lifetime, so postings
  never enter the Go heap; each shard loads via `diskstore.LoadMmapDeduped`
  (sharing a corpus-wide content-store mapping) for a deduped (`MOEDEX05`) shard
  dir, or `diskstore.LoadMmap` for a legacy inlined-content shard. `Regex`/`Literal` fan a per-shard
  scan across all shards (bounded by `NumCPU`) and merge the results; because
  `search.Match` carries absolute/repo/relative paths, matches from independent
  shards merge by concatenation with no cross-shard blob-ID space to reconcile. This
  backs `moedex-serve -http` (a small JSON `/search` API) and `-q` (one-shot).
- **Ranked agent context** ([`server.RankCorpus`](internal/server/rankcorpus.go)).
  `OpenRank` loads only blob *content* from every shard (`diskstore.LoadBlobs` — no
  positional postings, so no RAM wall), concatenates it into one content-only index
  with global blob IDs, and builds the corpus-wide BM25 token index and polyglot
  symbol index, fused by `rank.Ranker` (with `UseTokenCandidates(true)`). It
  implements `mcp.ContextSearcher`, so `moedex-serve -mcp` serves `search_context`
  over the whole corpus.
- **Sidecar persistence.** `OpenRank` is **load-or-build-and-save** for all three
  ranking sidecars: the BM25 token index (default `corpus-tokens.tki`, `TKI1`), the
  symbol index (default `corpus-symbols.sym`, `SYM2`), and (when an embedder is set)
  the embedding store (`corpus-embeddings.store`, `MDXE`). Each is reused only if its
  `.meta` validator matches the current corpus fingerprint; otherwise it is rebuilt
  and re-persisted (best-effort — a failed cache write never fails a boot).
  `BuildSidecars` lets the offline indexer pre-warm the token+symbol caches (it
  shares `loadUnified` with `OpenRank`, so blob IDs and the fingerprint match
  byte-for-byte); embeddings are a serve-time concern and are not built there.
- **Freshness** ([`internal/parity/manifest.go`](internal/parity/manifest.go)).
  `moedex-index build` writes a `manifest.json` recording, per shard, which repos
  contributed blobs, and per repo its git HEAD at ingest. `check` compares each
  repo's current `git rev-parse HEAD` against the manifest (`DetectChanges`);
  `refresh` rebuilds **only the shards whose repo set intersects the changed repos**
  (`Rebuild`), carries the untouched shards forward byte-for-byte, atomically swaps
  the new dir into place, and rebuilds the token/symbol sidecars. Freshness is
  shard-level (not per-repo) because a content-sized shard interleaves several repos;
  the documented caveats (shard-boundary drift, opaque per-build shard IDs) live in
  the package doc.
- **Daemon hardening** ([`cmd/moedex-serve`](cmd/moedex-serve)). The `-http` server
  defends a hostile network: optional **bearer auth** (`Authorization: Bearer`,
  enabled by `-auth-token` or `MOEDEX_AUTH_TOKEN`; `/healthz` and `/metrics` stay
  open), an unconditional **loopback-default bind** (a bare host/port rewrites to
  `127.0.0.1`, token or not), optional **TLS**
  (`-tls-cert`/`-tls-key`), structured logging via **`log/slog`** plus a `/metrics`
  endpoint, a panic-recovery + per-request-timeout middleware chain, and bounded
  `http.Server` read/write/idle timeouts. **SIGHUP** hot-swaps the served corpus (or
  re-runs `OpenRank` for `-mcp`) without dropping in-flight requests; a failed reload
  keeps the current one. The dense embedder for `-mcp` is selected by `-embed`
  (`auto|onnx|http|none`): `auto` picks `onnx` when an ONNX Runtime library path is
  given, else `http` when `MOEDEX_EMBED_URL` is set, else `none`.

## Corpus acquisition & freshness (`internal/corpus` + `cmd/moedex-corpus`)

Everything above assumes the corpus is already on disk. `moedex-corpus` is the
setup-and-freshness operator that *puts* it there and keeps it current — the one
component that reaches outside the box, to TurnCommerce's internal GitLab
(`gitlab.tcdevops.com`, and only that host). It is deliberately quarantined from
the engine: it lives in its own package, shells out to `glab`, `git`, and the
`moedex-index` binary behind a `Runner` seam (so all of its logic is unit-tested
without a network), and is **never imported by** `internal/*` or the daemon — the
engine's pure-Go, zero-dependency posture is untouched.

- **doctor** ([`doctor.go`](internal/corpus/doctor.go)) — preflight: is `glab`
  installed, authenticated to the host (delegated entirely to glab — moedex never
  handles tokens), and is `git` present? It reports the projected repo count and,
  on failure, the exact remediation (`glab auth login --hostname …`).
- **clone** ([`clone.go`](internal/corpus/clone.go)) — enumerate the curated
  projects (a top-level-group allowlist over all *visible* non-archived projects,
  which reproduces today's ~484-repo mirror; the default list is embedded), then
  shallow-clone (`--depth 1 --single-branch`, LFS skipped, non-interactive ssh)
  each into `<root>/<path_with_namespace>` with a bounded-concurrency worker pool.
  Idempotent.
- **sync** ([`sync.go`](internal/corpus/sync.go)) — reconcile the enumerated set
  against disk into clone / update / missing (missing is scoped to the allowlist,
  so narrowing it never prunes out-of-scope repos), then clone the new and
  fetch-and-reset the existing (shallow-safe, change-detected) concurrently, with
  optional `-prune` of the gone-on-server repos.
- **reindex** ([`reindex.go`](internal/corpus/reindex.go)) — `clone`/`sync
  -reindex` drive the per-blob-delta path through the `moedex-index` binary:
  `cas-build` the first time, else `cas-refresh`, then `cas-export -deduped`
  (delta-aware), then an optional daemon reload.

`cmd/moedex-corpus` is the thin CLI; `deploy/moedex-sync.{service,timer}` run
`sync -reindex` hourly. The mascot — Moe, an eight-tentacled octopus — is the
tool's voice (the parallel clones are his tentacles).

## Current state & deliberately deferred

**Built and working today:**

- In-memory positional byte-trigram index with SHA content dedup.
- Full Cox-style regex → boolean trigram reduction, verified to ripgrep parity.
- On-disk persistence (`MOEDEX03`) with mmap'd compact varint-delta postings.
- A four-arm hybrid ranker fused via RRF: BM25 lexical (over a persistent token
  index), a zoekt-style **path/filename arm** (on by default), a **symbol-name arm**
  (when a symbol index is installed), and an optional **dense arm** gated by query
  length — each arm independently gated so it stays additive.
- An optional dense arm with two interchangeable backends: an **in-process ONNX**
  code embedder (`-tags onnx`) and a **local HTTP** embedding server.
- Token-budgeted, deduplicated, block-scoped context-window assembly.
- A **polyglot** syntactic symbol layer (`symbol.BuildMulti`) with extractors for Go
  (`go/parser`), C#, TypeScript, SQL, and ColdFusion — scopes context blocks to real
  definition boundaries and feeds the symbol-name ranking arm.
- MCP `search_context` tool over stdio JSON-RPC (single-repo via `cmd/moedex-mcp`,
  whole-corpus via `moedex-serve -mcp`).
- A **warm multi-shard serving spine** (`internal/server`, `cmd/moedex-serve`): an
  mmap'd retrieval daemon (`-http`/`-q`) and a ranked agent-context surface (`-mcp`),
  with load-or-build-and-save BM25/symbol/embedding sidecars and daemon hardening
  (bearer auth, loopback default, TLS, slog/metrics, timeouts, SIGHUP hot-reload).
- **Shard-level freshness** (`internal/parity` manifest + `cmd/moedex-index`
  `build`/`check`/`refresh`): detect changed repos by git HEAD and rebuild only the
  affected shards.
- **Corpus acquisition + freshness** (`internal/corpus` + `cmd/moedex-corpus`): a
  setup tool that checks/guides glab auth (gitlab.tcdevops.com only), shallow-clones
  the curated repo set using the operator's own access, and on a schedule pulls
  fresh + drives the per-blob-delta reindex (`cas-refresh` → `cas-export -deduped`)
  + reloads the daemon. Shells out to glab/git/moedex-index; the engine never
  imports it.
- **Content-addressable store with global dedup + per-blob delta** (`internal/blobstore`
  + `cmd/moedex-index` `cas-build`/`cas-refresh`/`cas-export`): a corpus-wide CAS
  keyed by git blob SHA stores each unique blob exactly once (idempotent `Put` is the
  cross-shard dedup primitive), and `cas-refresh` re-ingests only a changed repo's
  net-new blobs (its co-resident repos are untouched — the win over `parity.Rebuild`).
  `cas-export` materializes a today-compatible servable shard dir from the CAS so the
  daemon and parity harness consume it unchanged.
- **Deduped served format** (`cas-export -deduped` → `MOEDEX05` content-less shards +
  one shared `blobs.dat` content store, `internal/diskstore`): the served corpus now
  inherits the CAS's cross-shard dedup — each unique blob's content is stored once
  corpus-wide (footprint ≈ the CAS `StoredBytes`) and `server.Corpus`/`RankCorpus`
  resolve it from one mmap'd shared store (content stays off the Go heap), returning
  byte-identical `(file,line)` matches. Proven parity-clean against the direct build
  and ripgrep by the deduped arm of `blobstore.TestCASExportParityCorpus`. Two
  silent-failure guards: the shared store is **self-verifying** at open (every entry
  re-hashed against its content-addressed key, so corruption fails the boot rather
  than serving wrong content; default-on, `MOEDEX_VERIFY_CONTENT=0` opts out), and the
  rank-sidecar fingerprint **folds in a content-true hash of `blobs.dat`'s directory**
  (its list of content-hash keys — `O(numBlobs)`, not `O(content bytes)`) so a
  content-store change invalidates stale token/symbol/embedding caches even when the
  shard files — and the store's total size and `MOECONT1` header — are unchanged.
- An IR-metrics evaluation harness (recall@k, precision@k, MRR, nDCG@k).
- A full-corpus exact-match retrieval parity harness (`internal/parity`,
  `cmd/moedex-parity`): sharded whole-corpus build, seeded ≥1000-query battery,
  ripgrep ground truth + independent gold adjudicator + Zoekt differential,
  gated by `make verify` and reported in `PARITY-REPORT.md`.

**LSP-precise navigation arm (`internal/navigate` + `cmd/moedex-nav`, `-tags lsp`)** —
the ADR 0017 spike for whether a future moedex could subsume Serena's navigation
role. It proves the three conditions ADR 0017 sets as the gate: real LSP
semantics (type-resolved go-to-def / find-refs / find-impls from an out-of-process
language server, not the syntactic symbol sidecar), Pool concurrency-safety (one
server per module root shared across parallel lanes, no thundering-herd spawn,
transparent restart of a dead server), and live working-tree/overlay freshness
(`didChange` re-sync, `SetOverlay` over an unsaved in-memory buffer, `NotifyChanged`
for disk edits). The JSON-RPC client is hand-written against the standard library
(`os/exec` + `encoding/json` over a Content-Length-framed stdio pipe), so the arm
adds **no new `go.mod` dependency** — the same build-tag boundary the dense
([0007](docs/adr/0007-optional-dense-arm.md)) and SIMD
([0013](docs/adr/0013-pure-go-defer-simd.md)) arms use. The language server is an
external binary on PATH, never linked. C# (`csharp-ls`), Go (`gopls`), SCSS/CSS
(`vscode-css-language-server`), and CFML (`cflsp`, built from softwareCobbler/cfc;
go-to-definition only) are proven live in tests; TypeScript/Python are too. SQL
(`sql-language-server`) is completion-only (no navigation in any SQL LSP) and HTML
(`vscode-html-language-server`) is shallow; both route and degrade gracefully.
Servers absent on a machine skip gracefully in tests. CFML requires a one-time
local build (`~/.moedex-tools/cfc`, wrapped as `cflsp` on PATH).
**Setup:** `make setup-lsp` (or `scripts/install-lsp-servers.sh`, `--with-cfml`
for the external CFML build) installs the servers; `moedex-index doctor` reports
which are present and their capability notes — both enumerate the engine's own
registry (`navigate.Servers()`) so they never drift from what the daemon routes.
`navigate.Pool.Stats()` exposes lifetime spawn/restart/eviction/query counters
(surfaced by `moedex-nav -stats`). This is a spike behind `-tags lsp`; the pure-Go
default build is untouched and gains none of it. See ADR 0017 for the full gate.
ADR 0018 closes the bidirectional name↔position mismatch this leaves for a
caller (e.g. Protostar's `CodebaseMapper`) holding a *name* rather than a
`file:line:col`: `find_symbol` (`workspace/symbol`, root-routed, root+lang or
merge-every-live-server-for-root when lang is omitted) and `symbols_overview`
(`textDocument/documentSymbol`, file-routed) are registered as MCP tools
alongside `find_definition`/`find_references`/`find_implementations` in
`cmd/moedex-serve/nav_lsp.go` and return named `Symbol{Name, Kind, Loc}` results
(one `name\tkind\tfile:line:col` line each) instead of a bare `Location`.

**Deliberately deferred (design intentions, not yet built)** — tracked in the
northstar and [`research/`](research):

- **Incremental / delta indexing** — a **per-blob delta path exists at the storage
  layer** (the content-addressable store, `internal/blobstore`, dedups blobs globally
  and `cas-refresh` adds only a changed repo's net-new blobs) **and the served format
  now dedups too**: `cas-export -deduped` writes content-less `MOEDEX05` shards that
  reference one shared content store (`blobs.dat`) by content hash, so the served
  corpus stores each unique blob's content once corpus-wide (footprint ≈ the CAS
  `StoredBytes`) instead of re-inlining it per shard, and the serving spine
  (`server.Corpus`/`RankCorpus`) resolves content from that one mmap'd store —
  validated parity-clean against the direct build and ripgrep by the deduped arm of
  `TestCASExportParityCorpus`. What remains: the legacy `moedex-index refresh` path is
  still shard-level (rebuilds whole affected shards), `cas-export` without `-deduped`
  still writes the inlined `MOEDEX03` bridge (kept as the proven default), and a
  delta-aware deduped re-export (append a changed repo's net-new content + rewrite only
  affected shards, instead of re-exporting the whole dir) is not yet built.
  **Compaction-GC is built**, though, as a separate cheap in-place reclaim pass:
  `moedex-index cas-compact` (`internal/blobstore/compact.go`'s `CompactCAS` and
  `CompactDedupedShardDir`) rewrites the CAS pack and/or the deduped `blobs.dat`
  from their own live entries — keyed off the blob manifest's / live shards'
  referenced-set — to drop content no repo references anymore, without
  re-ingesting from git or re-exporting from the CAS.
- **Distribution / sharding** — sharded on disk and served as a multi-shard corpus,
  but still **single-node**: there is no cross-node distribution or replication.
- **Native SIMD intersection/verification kernel** — see
  [`research/simd-kernel.md`](research/simd-kernel.md); the engine is pure Go.
  The clean kernel boundary now exists: `internal/setops` owns the sorted-uint64
  set algebra (the trigram AND/OR fold), and `internal/query` calls it instead of
  hand-rolling intersect/union. The pure-Go path is the always-built default on
  every arch (galloping + caller-reusable buffers — measured ~26x faster and
  zero-alloc vs the old per-fold-allocating merge on the rare-AND-common posting
  shape). An optional native AVX2 kernel (`simd/archsimd`, no cgo, no go.mod dep)
  lives behind `-tags moedex_simd` (amd64 + `GOEXPERIMENT=simd`); `make build-simd`
  builds it. Honest measured result (Rosetta-translated x86-64, directional only):
  **SIMD shows no consistent win at this corpus scale** — the pure-Go galloping
  path beats the AVX2 broadcast-compare on the dominant skewed case — confirming
  the research note's prediction that the latency tail is scan-bound, not
  intersection-bound. Native-amd64 ns/op + the verification-side (Teddy-class)
  SIMD prefilter remain deferred. The engine ships pure Go by default.
- **FM-index compressed cold tier** — see
  [`research/fm-index-cold-tier.md`](research/fm-index-cold-tier.md).
- **Learned reranker** — RRF over the four arms is the current *default* fusion;
  see [`research/learned-reranker.md`](research/learned-reranker.md). A linear
  post-fusion reranker mode is already built (`internal/rank/reranker.go`'s
  `Fusion`/`LinearReranker`, wired via `Ranker.SetFusion`/`SetReranker`) but no
  production caller opts into `FusionLinear` yet, and a GBDT upgrade remains
  deferred behind a future build tag.
- **Deeper / semantic symbols** — the polyglot extractors (Go/C#/TS/SQL/CFML) are
  syntactic and best-effort (the non-Go ones are regex/byte scanners, not full
  parsers; see [`research/symbol-layer.md`](research/symbol-layer.md)). Tree-sitter
  or SCIP-grade parsing, and additional languages, remain future work.
- **Find-refs / go-to-def and an ANN vector index** are still future work *in the
  default build*. (The symbol-name ranking arm and the path/filename arm are now
  built — see the ranker.) A `-tags lsp` spike now exists for type-resolved
  navigation — see "LSP-precise navigation arm" below and ADR 0017 — but the
  pure-Go default binary is unchanged and gains neither.

---

## Build & run

```sh
go build ./...        # build all packages and commands (default: zero ML deps)
go test ./...         # run the test suite (parity tests shell out to `rg`)
make verify           # master gate: health + round-trip + full-corpus parity
make parity           # just the full-corpus parity gate -> PARITY-REPORT.md
make setup            # one-time: install the Zoekt differential oracle
make build-dense      # build moedex-serve with the in-process ONNX embedder (-tags onnx)
make test-dense       # run the onnx-tagged embedder test (needs the ONNX Runtime lib)
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

The server ingests the repo, builds the trigram + token indexes and the polyglot
symbol layer (`symbol.BuildMulti`), optionally builds the dense store, and serves the
`search_context` tool (args: `query` required, `token_budget` and `top_k` optional)
over stdin/stdout. `cmd/moedex-mcp` uses the **HTTP** dense backend only; the
in-process ONNX backend is wired in `moedex-serve` (below).

**Dense arm (optional) — environment variables:**

| Variable | Effect |
|---|---|
| `MOEDEX_EMBED_URL` | Base URL of a local OpenAI/ollama-style embeddings endpoint (e.g. `http://localhost:11434/v1`). Setting it enables the dense arm; `HTTPEmbedder.Embed` POSTs to `{URL}/embeddings`. |
| `MOEDEX_EMBED_MODEL` | Model name sent in the embedding request body. |

Without `MOEDEX_EMBED_URL` the server logs that the dense arm is disabled and runs
lexical + symbol + path ranking with zero external dependencies.

### `moedex-index` — offline shard-dir builder / freshness

```sh
moedex-index build   -corpus ROOT -shard-dir DIR [-shard-bytes N] [-force] [-v]
moedex-index check   -shard-dir DIR [-corpus ROOT]
moedex-index refresh -shard-dir DIR [-corpus ROOT] [-keep-backup] [-v]
```

`build` indexes every git repo under `-corpus` into byte-sized `shard-NNNN.idx`
files (default `-shard-bytes` is `parity.DefaultShardBytes` ≈ 150MB of content),
writes the `manifest.json` freshness sidecar, and pre-warms the token+symbol ranking
sidecars. `check` is read-only and prints changed/added/removed repos vs. the
manifest's recorded git HEADs. `refresh` rebuilds only the affected shards and
atomically swaps them in (the previous dir is dropped unless `-keep-backup`). For
`check`/`refresh` the corpus root defaults to the `Root` recorded in the manifest.
The result is directly servable by `moedex-serve -shard-dir DIR`.

The content-addressable family operates the CAS (`internal/blobstore`):

```sh
moedex-index cas-build   -corpus ROOT -cas-dir DIR
moedex-index cas-refresh -cas-dir DIR [-corpus ROOT]
moedex-index cas-export  -cas-dir DIR -shard-dir OUT [-shard-bytes N] [-force] [-deduped]
moedex-index cas-compact [-cas-dir DIR] [-shard-dir DIR]
```

`cas-build` ingests every repo into a global content-addressed blob store, storing
each unique blob once for the whole corpus (cross-shard dedup) and writing the
`blobmanifest.json` repo→blobset sidecar; it prints the dedup ratio (raw bytes /
stored bytes). `cas-refresh` diffs each repo's current blob set against the manifest
and physically appends only net-new blobs (per-blob delta — co-resident repos are not
re-ingested), printing the blobs/bytes added and the dedup no-op Puts skipped; the
corpus root defaults to the manifest's `Root`. `cas-export` materializes a
servable shard dir + `manifest.json` from the CAS so `moedex-serve` and the parity
harness consume it unchanged. By default it writes the parity-preserving
inlined-content bridge (`MOEDEX03`, content re-inlined per shard exactly as a direct
`build`). With **`-deduped`** it writes the deduped served format instead:
content-less `MOEDEX05` shards plus one shared `blobs.dat` content store, so each
unique blob's content is stored once corpus-wide (footprint ≈ the CAS `StoredBytes`)
rather than re-inlined per shard. `server.Open`/`OpenRank` auto-detect the shared
store and resolve content from it — returning byte-identical `(file,line)` matches
(parity-validated against the direct build and ripgrep). `cas-compact` reclaims
dead (unreferenced) content in place — rewriting the CAS pack (`-cas-dir`) and/or
the deduped served store (`-shard-dir`) from their own live entries, keeping only
blobs the manifest/live shards still reference — without re-ingesting from git or
re-exporting from the CAS; pass either or both flags.

### `moedex-serve` — warm retrieval / context daemon

```sh
moedex-serve -shard-dir DIR -http :8080          # retrieval HTTP API (GET /search)
moedex-serve -shard-dir DIR -q PATTERN [-regex]  # one-shot retrieval query
moedex-serve -shard-dir DIR -mcp                 # ranked agent context (MCP/stdio)
```

| Flag | Default | Meaning |
|---|---|---|
| `-shard-dir` | `MOEDEX_SHARD_DIR` | directory of prebuilt `*.idx` shards (required) |
| `-http` | _(unset)_ | serve the retrieval HTTP API on this address |
| `-mcp` | `false` | serve ranked agent context over MCP (stdio) |
| `-q` | _(unset)_ | one-shot retrieval query |
| `-regex` | `false` | treat `-q` as a regular expression |
| `-top-k` | `20` | default ranked results per MCP query |
| `-embed` | `auto` | dense embedder for `-mcp`: `auto`/`onnx`/`http`/`none` |
| `-onnx-runtime` | `ONNXRUNTIME_LIB_PATH` | path to the ONNX Runtime shared library (in-process embedder; needs `-tags onnx`) |
| `-auth-token` | `MOEDEX_AUTH_TOKEN` | require `Authorization: Bearer <token>` on `-http` (except `/healthz`, `/metrics`) |
| `-tls-cert` / `-tls-key` | _(unset)_ | serve `-http` over HTTPS (set together) |
| `-request-timeout` | `30s` | per-request HTTP timeout on `-http` |
| `-limit` | `0` | cap matches printed/returned (0 = no cap) |

The `-http` server exposes `GET /search?q=&regex=&limit=`, plus `/healthz`,
`/metrics`, and `/stats`. SIGHUP hot-reloads the shard dir without dropping requests.
The dense arm applies to `-mcp` only; with `-embed onnx` (and a `-tags onnx` build)
the embeddings are computed in-process and persisted next to the shards.

### `scale` — corpus sizing tool

```sh
scale ROOT [sampleRegex]          # MOEDEX_MMAP=1 to also measure mmap-loaded heap
```

Walks `ROOT` for git repos, indexes them all into one in-memory index, and reports
file/blob counts, dedup ratio, trigram/posting counts, build throughput, and heap
usage as a multiple of content size. With `MOEDEX_MMAP=1` it persists the index,
drops the in-RAM copy, reloads with postings mmap'd, and reports the heap
difference.
