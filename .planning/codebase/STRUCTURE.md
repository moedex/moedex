# Codebase Structure

**Analysis Date:** 2026-08-24

## Directory Layout

```
moedex/
├── cmd/                          # Executables — thin main packages only
│   ├── moedex/                   # One-shot search CLI
│   ├── moedex-mcp/               # Single-repo MCP server (stdio)
│   ├── moedex-serve/             # Warm daemon: -http / -mcp / -mcp-http / -q
│   ├── moedex-index/             # Offline shard/CAS/graph builder + freshness
│   ├── moedex-corpus/            # Corpus acquisition + freshness over glab/git
│   ├── moedex-parity/            # Full-corpus ripgrep parity gate
│   ├── moedex-nav/               # LSP navigation CLI (-tags lsp)
│   └── scale/                    # Corpus sizing / throughput tool
├── internal/                     # All library code (module-private)
│   ├── trigram/                  # Positional-trigram primitive
│   ├── fold/                     # ASCII case-fold variants
│   ├── setops/                   # Sorted-set intersect/union kernel
│   ├── index/                    # In-memory content-addressed trigram index
│   ├── ingest/                   # git ls-files + .ai-privacy.yml gating
│   ├── query/                    # Regex → boolean trigram (Cox reduction)
│   ├── search/                   # Candidate retrieval + regex verify
│   ├── diskstore/                # Index persistence, mmap, deduped format
│   ├── blobstore/                # Corpus-wide CAS + delta refresh + export
│   ├── tokenindex/               # BM25 term statistics (frozen Tokenize)
│   ├── symbol/                   # Polyglot symbol layer + corpus name lookup
│   ├── classify/                 # Syntactic → architectural kind promotion
│   ├── embed/                    # Optional dense arm (ONNX / HTTP)
│   ├── rank/                     # RRF multi-arm fusion
│   ├── contextwin/               # Token-budgeted context assembly
│   ├── graph/                    # Shared confidence + evidence types
│   │   ├── candidates/           # Recall-complete edge candidates
│   │   ├── verify/               # Regex tier: Candidate → Pattern
│   │   ├── diskgraph/            # mmap-served adjacency sidecar
│   │   ├── httproute/            # HTTP route edge extraction
│   │   ├── cluster/              # Cluster/community grouping
│   │   └── manifest/             # Graph manifest schema
│   ├── server/                   # Warm serving spine + graph orchestration
│   ├── mcp/                      # MCP contracts, SDK server, result identity
│   ├── snapshot/                 # Atomic immutable index generations
│   ├── navigate/                 # LSP navigation arm (-tags lsp)
│   ├── corpus/                   # glab/git acquisition (quarantined)
│   │   └── catalog/              # Leaf managed-corpus schema (no os/exec)
│   ├── parity/                   # Full-corpus parity harness + freshness manifest
│   ├── eval/                     # IR metrics + gold gates
│   │   └── testdata/graph-corpus/ # Hermetic multi-language fixture corpus
│   ├── fmindex/                  # FM-index cold-tier experiment
│   └── version/                  # Build identity
│       ├── buildmeta/
│       └── sourcehash/
├── docs/                         # Authoritative design record
│   ├── adr/                      # 0001–0022 architecture decision records
│   └── plans/phases/             # Numbered implementation phase plans
├── research/                     # Exploratory notes (NOT current behavior)
├── deploy/                       # systemd units + launchd plists + env example
├── scripts/                      # Install, refresh, benchmark shell scripts
├── ARCHITECTURE.md               # Single most authoritative technical reference
├── CLAUDE.md                     # Agent guidance / invariants
├── AGENTS.md                     # Repository guidelines
├── README.md                     # Operator-facing entry point
├── Makefile                      # All gates: health, verify, parity, tagged builds
├── go.mod / go.sum               # Single module `moedex`, Go 1.26
├── Dockerfile / Dockerfile.dense # Default and ONNX container builds
└── .gitlab-ci.yml                # health on every push/MR; parity on schedule/tag
```

## Directory Purposes

**`cmd/`:**
- Purpose: one directory per executable, each a `package main`
- Contains: flag parsing, subcommand dispatch, wiring of `internal/` packages, stdout/stderr formatting
- Key files: `cmd/moedex-serve/main.go` (daemon, largest at 924 lines), `cmd/moedex-index/main.go` (subcommand dispatch, 994 lines), `cmd/moedex-corpus/main.go`
- Rule: command packages stay thin — reusable behavior belongs in `internal/`

**`internal/` (leaf packages):**
- Purpose: dependency-free primitives every layer can use
- Contains: `trigram`, `fold`, `setops`, `graph`, `snapshot`, `fmindex`, `corpus/catalog`, `version`
- Key files: `internal/trigram/trigram.go` (19 lines), `internal/graph/model.go`

**`internal/` (engine):**
- Purpose: the retrieval and ranking core
- Contains: `index`, `ingest`, `query`, `search`, `diskstore`, `blobstore`, `tokenindex`, `symbol`, `rank`, `contextwin`, `embed`, `classify`
- Key files: `internal/index/index.go`, `internal/search/search.go`, `internal/rank/ranker.go`, `internal/contextwin/contextwin.go`

**`internal/server/`:**
- Purpose: the warm multi-shard serving spine and graph orchestration — the single top-of-DAG package
- Contains: 50 flat files, ~17.8k lines; no subdirectories
- Key files: `internal/server/corpus.go` (retrieval), `internal/server/rankcorpus.go` (ranked context + sidecars), `internal/server/symbolcorpus.go`, `internal/server/graphtools.go` (MCP graph tools), `internal/server/graphbuild.go`, `internal/server/graphneighbors.go`

**`internal/graph/`:**
- Purpose: the only part of `internal/` with subpackages, split by graph construction phase
- Contains: shared model at the root, phase packages below it
- Key files: `internal/graph/model.go`, `internal/graph/candidates/`, `internal/graph/verify/`, `internal/graph/diskgraph/diskgraph.go`

**`internal/corpus/`:**
- Purpose: quarantined acquisition — the only package that shells out to `glab`/`git`
- Contains: `Runner` seam, doctor/clone/sync/reindex, managed-corpus locking
- Key files: `internal/corpus/runner.go`, `internal/corpus/clone.go`, `internal/corpus/sync.go`, `internal/corpus/managed_sync.go`
- Rule: **never imported** by `internal/*` engine packages or the daemon; the engine uses `internal/corpus/catalog` instead

**`docs/adr/`:**
- Purpose: the decision record — why each architectural choice was made, with evidence
- Contains: `0001`–`0022` plus `README.md` index
- Rule: read the relevant ADR before changing what it governs

**`docs/plans/phases/`:**
- Purpose: numbered implementation phase plans (`NN-kebab-name/PLAN.md`, some with `SUMMARY.md` / `ROLLOUT.md`)

**`research/`:**
- Purpose: exploratory design notes and hypotheses
- Rule: **not** a description of current behavior — `ARCHITECTURE.md` and the ADRs supersede it

**`deploy/`:**
- Purpose: service definitions for both platforms
- Key files: `deploy/moedex-serve.service`, `deploy/moedex-refresh.{service,timer}`, `deploy/moedex-sync.{service,timer}`, `deploy/com.moedex.{serve,refresh}.plist`, `deploy/moedex-serve.env.example`

**`scripts/`:**
- Purpose: operator and benchmark shell scripts
- Key files: `scripts/install-macos.sh`, `scripts/refresh-corpus.sh`, `scripts/install-lsp-servers.sh`, `scripts/bench-latency.sh`, `scripts/managed-refresh-test.sh`

## Key File Locations

**Entry Points:**
- `cmd/moedex-serve/main.go:51`: daemon main; config file loaded before flag defaults
- `cmd/moedex-index/main.go:83`: offline builder subcommand dispatch
- `cmd/moedex-corpus/main.go:38`: acquisition subcommand dispatch
- `cmd/moedex-mcp/main.go`: single-repo MCP wiring (the clearest end-to-end example)
- `cmd/moedex/main.go`: smallest complete ingest → index → search example (66 lines)

**Configuration:**
- `Makefile`: every gate and tagged build (`health`, `verify`, `parity`, `roundtrip`, `graph-eval`, `build-dense`, `build-simd`, `build-lsp`, `install-*`)
- `.gitlab-ci.yml`: `make health` on push/MR, `make parity` on schedules/tags
- `go.mod`: module `moedex`, Go 1.26, with a `replace` pinning the tokenizer fork
- `.gitignore`: excludes `.parity-work*`, root-built binaries, `.claude/worktrees/`, `test.log`
- `deploy/moedex-serve.env.example`: the template for daemon service configuration
- `.vscode/settings.json`: editor settings

**Core Logic:**
- `internal/index/index.go:165`: `AddFile` — SHA dedup + positional posting emission
- `internal/search/search.go:86`/`:208`: `Literal` / `Regex` retrieval entry points
- `internal/query/query.go:35`: the `Query` interface (Cox reduction output)
- `internal/rank/ranker.go:18`: `Config` with every arm gate and its measured rationale
- `internal/contextwin/contextwin.go:38`: token budget and block-selection constants
- `internal/mcp/contracts.go:206`: per-tool argument contract
- `internal/server/graphtools.go:412`: the stable `tools/list` order

**Testing:**
- `*_test.go` colocated beside every implementation file (no separate test tree)
- `internal/search/parity_test.go`: shells out to `rg` for line-level parity
- `internal/parity/`: full-corpus harness (`Build`, `Generate`, three oracles, report)
- `internal/eval/testdata/graph-corpus/`: the only `testdata` directory — a hermetic Go/TS/C# fixture corpus
- `internal/ingest/source_test.go:28`: architecture guard — fails if `ingest` gains a production dependency on `internal/corpus`

**On-disk artifacts (produced, not committed):**
- `*.idx` shards + `blobs.dat` content store (shard dir)
- `manifest.json` (freshness), `corpus-tokens.tki`, `corpus-symbols.sym`, `corpus-embeddings.store`, `corpus-graph.graph`, `*.meta` validators
- `blobs.pack` / `blobs.idx` / `blobmanifest.json` (CAS)
- `snapshots/`, `CURRENT`, `snapshot.json` (atomic generations)
- `PARITY-REPORT.md`, `test.log` (gate output; `test.log` is gitignored)

## Naming Conventions

**Files:**
- Package-named primary file: `blobstore.go`, `contextwin.go`, `snapshot.go`, `symbol.go`
- Binary serialization: `codec.go` (`internal/index`, `internal/embed`, `internal/symbol`, `internal/tokenindex`)
- Per-language extractor: `extract_<lang>.go` — `extract_cs.go`, `extract_ts.go`, `extract_sql.go`, `extract_cf.go`
- Build-tag capability pair: `<feature>.go` + `<feature>_disabled.go`, or `<feature>_<tag>.go` + `<feature>_<tag>_disabled.go` — `onnx.go`/`onnx_disabled.go`, `lsp.go`/`lsp_disabled.go`, `graphcalls_lsp.go`/`graphcalls_disabled.go`, `graphsimilar_onnx.go`/`graphsimilar_disabled.go`
- Platform/arch variants use Go's implicit suffixes: `setops_simd_amd64.go`, `lock_unix.go`, `lock_other.go`
- Feature-topic files inside a flat package: `internal/server/graph<topic>.go` (`graphbuild`, `graphtools`, `graphneighbors`, `graphrefresh`, `graphdiscovery`, `graphhierarchy`, `graphinjection`, `graphrenders`, `graphqueries`, `graphcallaudit`)
- Tests: `<file>_test.go` colocated; benchmarks live in `<file>_bench_test.go`

**Directories:**
- Lower-case, single word, no underscores or hyphens: `tokenindex`, `blobstore`, `contextwin`, `diskgraph`
- Executables are hyphenated after the tool name: `moedex-serve`, `moedex-index`, `moedex-corpus`
- Phase plan directories are `NN-kebab-name/`: `docs/plans/phases/04-branch-aware-cas/`
- ADRs are `NNNN-kebab-title.md`: `docs/adr/0006-rrf-hybrid-ranking.md`

**Identifiers:**
- `MixedCaps` exported, short receiver names, package-prefixed error strings (`"snapshot: no current snapshot"`)
- Capability flags are `const <Feature>Compiled bool` (`embed.ONNXCompiled`, `navigate.LSPCompiled`)
- Format versions are exported constants near their codec (`symbol.ExtractorsVersion`, `snapshot.FormatVersion`)
- Environment variables are `MOEDEX_*` (about 50 in use)

## Where to Add New Code

**New retrieval or ranking behavior:**
- Primary code: the owning `internal/` package (`internal/search`, `internal/rank`, `internal/query`)
- Tests: colocated `_test.go` in the same package
- Gate: `make verify` must stay green for anything touching `query`, `search`, or `parity`

**New language symbol extractor:**
- Implementation: `internal/symbol/extract_<lang>.go` implementing `symbol.Extractor`
- Registration: add the extension case to `ExtractorForPath` in `internal/symbol/build_multi.go:21`
- Required follow-up: bump `ExtractorsVersion` in `internal/symbol/extract.go:35` so cached symbol sidecars rebuild
- Tests: `internal/symbol/extract_<lang>_test.go`

**New MCP tool:**
- Contract: declare name + arguments in `internal/mcp/contracts.go`
- Handler: implement `mcp.ToolHandler` in `internal/server` (graph tools live in `internal/server/graphtools.go`)
- Registration: add to the stable order in `GraphToolset.Tools()` (`internal/server/graphtools.go:412`)
- Tests: `internal/mcp/contract_test.go` for the contract, `internal/server/*_test.go` for behavior

**New graph edge type or extraction rule:**
- Extraction: `internal/graph/candidates/` (recall) then `internal/graph/verify/` (precision)
- Persistence: `internal/graph/diskgraph/diskgraph.go` (`EdgeType`, fixed-width record)
- Traversal lane: `internal/server/graphneighbors.go:55` (`neighborSpecs`) and `internal/server/graphtools.go`
- Confidence: reuse `graph.ConfidenceTier` from `internal/graph/model.go` — do not invent a parallel scale

**New on-disk format or format version:**
- Writer/reader: a `codec.go` (or dedicated file) in the owning package, with a magic + version header
- Rule: little-endian, temp+rename after fsync, and a documented row in the `ARCHITECTURE.md` formats table

**New optional capability with an external dependency:**
- Real implementation: `<feature>.go` behind `//go:build <tag>`
- Stub: `<feature>_disabled.go` behind `//go:build !<tag>` with the identical exported API, a `const <Feature>Compiled = false`, and a sentinel error naming the tag
- Build target: add `build-<tag>` / `test-<tag>` / `vet-<tag>` to the `Makefile`
- Rule: the default `go build ./...` must stay pure Go standard library

**New CLI subcommand:**
- Dispatch: the `switch` in the relevant `cmd/*/main.go`
- Runner: a `run<Name>(args []string) error` function in the same package (split into a sibling file if large, as `cmd/moedex-index/snapshot.go` and `cmd/moedex-serve/config.go` do)

**New shared helper:**
- If dependency-free: a new leaf package under `internal/` (follow `internal/fold`)
- If it needs `os/exec` against GitLab: `internal/corpus` only — and route any schema the engine must read through `internal/corpus/catalog`

**New documentation:**
- Decision with alternatives weighed: `docs/adr/NNNN-kebab-title.md` plus a line in `docs/adr/README.md`
- Current behavior: `ARCHITECTURE.md`
- Exploratory or unvalidated: `research/`

## Special Directories

**`internal/eval/testdata/`:**
- Purpose: hermetic multi-language fixture corpus (`graph-corpus/app`, `/web`, `/events`) for the graph gold gate
- Generated: No
- Committed: Yes

**`.parity-work/`:**
- Purpose: scratch space for the full-corpus parity harness; dot-prefixed so `go build/vet ./...` skip it
- Generated: Yes
- Committed: No (gitignored)

**`.claude/worktrees/`:**
- Purpose: transient agent worktrees, each a full checkout of the repo
- Generated: Yes
- Committed: No (gitignored) — exclude from any repo-wide scan or it triples every match count

**`.planning/`:**
- Purpose: Moe planning artifacts, including this codebase map
- Generated: Yes
- Committed: Per project convention

**`docs/adr/`:**
- Purpose: append-only decision record
- Generated: No
- Committed: Yes

**`research/`:**
- Purpose: exploratory notes retained with their original caveats
- Generated: No
- Committed: Yes — but superseded by the ADRs where they conflict

---

*Structure analysis: 2026-08-24*
