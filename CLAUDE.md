# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

moedex is a clean-room, single-node trigram **code-search engine and agent context API** in Go 1.26 (`module moedex`). It keeps Zoekt's positional-trigram retrieval core and replaces everything else: content is addressed by **git blob SHA** (identical content indexed once), trigrams are **positional byte-trigrams** (byte offsets, not rune offsets), and a regex is reduced to a **necessary-condition boolean trigram query** that selects candidate blobs which a real regex engine then verifies.

Start with **`README.md`** for the operator-facing entry point. The single most authoritative technical reference is **`ARCHITECTURE.md`** (the code as it actually exists), with decisions recorded as ADRs in **`docs/adr/`**. Read those before non-trivial changes; `research/` holds exploratory design notes rather than current behavior.

## Commands

```bash
make health        # format/import fences + build + vet + full unit suite
make build         # go build ./...
make vet           # go vet ./...
make test          # go test ./...  (writes test.log; warns on skipped tests)
make verify        # MASTER gate (DoD): health + round-trip + full-corpus parity. Non-zero on any failure.
make parity        # full-corpus exact-match parity vs ripgrep -> PARITY-REPORT.md
make roundtrip     # persistence save/load identity test

# Run a single test:
go test ./internal/search/ -run TestRegex -count=1
go test ./internal/parity/ -run 'RoundTrip|MoedexEqualsGold' -count=1
```

CI (`.gitlab-ci.yml`): `make health` runs on every push/MR; `make parity` runs only on schedules/tags on a self-hosted runner that has the corpus + `rg`.

### Build-tagged / optional arms (NOT in the default build)

- **Dense (ONNX) embedder** — `make build-dense` / `go build -tags onnx`. The `onnx` tag is the *only* thing that pulls `sugarme/tokenizer` + `yalue/onnxruntime_go`. Run needs `ONNXRUNTIME_LIB_PATH`. Test: `make test-dense` (skips without the lib).
- **SIMD set-ops kernel** — `make build-simd` (amd64 + `GOEXPERIMENT=simd`, `-tags moedex_simd`). `make vet-simd` cross-compiles it without an amd64 host. The default build stays pure Go on every arch.

## Key invariants (do not break)

- **The default engine is pure Go with zero ML/runtime dependencies.** Shell dependencies are confined to `internal/cli`, `internal/tui`, `internal/render`, and `internal/ui/theme`; only the `onnx` build tag may activate the in-process embedder and only `moedex_simd` may use archsimd.
- **ripgrep parity: moedex never under-approximates.** The trigram→regex (Cox) reduction must only ever produce a *necessary* condition; candidate blobs are always verified by a real regex engine. Parity is pinned by `internal/search/parity_test.go` (shells out to `rg`) and the full-corpus `internal/parity` harness. Any change to `internal/query`, `internal/search`, or `internal/parity` must keep `make verify` green.
- **Content identity = git blob SHA.** Dedup happens by SHA across the whole corpus (`internal/blobstore` CAS). Binary blobs (NUL byte) are skipped and a leading UTF-8 BOM is stripped to stay aligned with ripgrep line/match boundaries.
- The `Tokenize` rule in `internal/tokenindex` is **frozen** (Unicode word-runs, camelCase/acronym case-split, lowercased) — it feeds BM25; changing it shifts ranking.

## Architecture map

All library code is under `internal/`; executables under `cmd/`. See `ARCHITECTURE.md` for the full package table and exported surface.

**Index build (Pipeline A):** `ingest.Repo` (`git ls-files -s -z`) → `index.AddFile` (SHA-dedup, emits positional `Posting{Blob,Offset}` already sorted) → sidecars built from the same in-memory index: `tokenindex.Build` (BM25), `embed.BuildStore` (optional dense vectors), `symbol.BuildMulti` (polyglot symbol ranges) → `diskstore.Save` / `LoadMmap` (postings stay on disk via mmap).

**Query / serving (Pipeline B):** MCP `search_context` → `mcp.IndexSearcher.SearchContext` → `rank.Ranker.Rank` fuses arms via **Reciprocal Rank Fusion (RRF)**: lexical (trigram candidates + BM25), path-token, symbol-name, optional dense → `contextwin.Assemble` expands salient line spans to enclosing symbol blocks within a token budget.

**Serving spine:** `internal/serve` (`Corpus` / `RankCorpus`) opens a multi-shard mmap'd index warm; `internal/graph/serve` owns online graph tools and `internal/graph/build` owns offline graph construction.

**Corpus lifecycle:** `internal/corpus` is the **only** package that shells out to `glab`/`git` for acquisition + freshness; it is **never imported by the engine or daemon**. Driven by `moedex corpus`, scoped to gitlab.tcdevops.com.

### Executable (`cmd/`)

`cmd/moe` is the only executable source. Semantic commands cover search, index,
graph, corpus, serving, MCP, navigation, parity, doctor, config, and version.
Former binary names are install-time compatibility symlinks for two releases.

## Corpus for parity tests

`make verify`/`make parity` and the `internal/parity` + `internal/search` parity tests need the real corpus on disk at `MOEDEX_CORPUS` (default `~/TCGitlab`) and `rg` on PATH. Override: `make verify MOEDEX_CORPUS=/path`. Without the corpus those tests skip (env-gated skips are acceptable). Parity scratch lives in the repo-local `.parity-work/` (dot-prefixed so `go build/vet ./...` skip it).
