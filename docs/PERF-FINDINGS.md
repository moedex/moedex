# moedex performance findings — agent query path

_Captured 2026-06-26 on Apple M5 Max, Go 1.26, against the live corpus
(`~/.moedex-index/shards`: 50,514 blobs / 50,514 docs / 25,508 symbol blobs,
**dense arm OFF** — `moedex_corpus_dense_chunks 0`)._

## TL;DR

A `search_context` call costs **~2.3s p50 / ~3.1s p95**. The cost is **not** the
embedding arm (it isn't running), the symbol arm, or context assembly. **88.7% of
all allocations and ~half the CPU come from one function: `rank.(*Ranker).lexicalSpans`**,
which lowercases and splits the *entire content of every candidate blob* on every
query — then the result is thrown away for all but the top 12. Fixing the obvious
case (compute spans only for the results we return) should cut latency by ~10×.

## STATUS — both fixes shipped (branch `perf/lexical-spans`)

`make health` green after each; ranking output unchanged (verified by code +
the eval NDCG suite). **Total: geomean −99.17%** (496.8 ms → 4.14 ms).

| query (e2e `SearchContext`) | before | fix #1 | **fix #1+#2** | total speedup |
|---|---|---|---|---|
| selective_ident | 2.16s | 0.210s | **9.9 ms** | **218×** |
| common_token | 1.22s | 0.203s | **3.1 ms** | 397× |
| multiword | 2.52s | 0.215s | **12.9 ms** | 195× |
| natural_phrase | 2.54s | 0.205s | **10.4 ms** | 244× |
| rare_symbol | 3.16s | 0.207s | **14.9 ms** | 212× |

Memory/query: ~1.5–1.8 GB → **~7 MB** (~200×). Allocations/query: ~21M → ~60K.

**Fix #1 — defer `lexicalSpans` to the survivors** (−95.8% geomean). Compute the
full-content span scan only for the ≤`topK` results returned, not all ~50K
candidates. Removed 88.7% of allocations; exposed the symbol arm as the residual.

**Fix #2 — symbol-name inverted index** (−80.2% of what #1 left). `symbolArm` no
longer scans the whole corpus per query: a one-time `buildSymbolIndex` (in
`SetSymbols`, at load) tokenizes every symbol name once into `symInfo` (per-blob
distinct subtokens + name line) and `symPostings` (subtoken → blobs). Queries now
gather candidates from the postings and read precomputed subtokens — never
re-tokenizing or scanning non-candidates. Identical results (every qualifying
symbol matches ≥1 query term, so its blob is always a candidate). The per-query
symbol cost moved to load (~one extra corpus tokenize pass, negligible vs the ~40s
cold start). This is the same technique `pathArm` already used.

**What's left in a query (3–15 ms):** legitimate retrieval — the BM25 candidate
scan (`lexicalArm`) and `pathArm`, both single-digit ms; symbol arm now <1 ms;
context assembly <1 ms. No single hotspot remains; further gains would be
incremental (e.g. lexical-arm allocation tuning), not structural.

_(The live launchd daemon still runs the pre-fix binary until rebuilt + reloaded.)_

---

## Original analysis (pre-fix)

## How to reproduce

```bash
make bench-real        # Go macro-benchmarks vs the real index (+ CPU/mem profiles in .bench/)
make bench-latency     # end-to-end p50/p95/p99 over the running warm HTTP daemon
go tool pprof -top -nodecount=25 .bench/cpu.prof
go tool pprof -list lexicalSpans .bench/cpu.prof
```

`bench-real` is opt-in (skips with no shard dir; CI never needs the multi-GB
index). The corpus loads once per run (~cold-start cost), then every benchmark
shares it.

## Raw numbers

### End-to-end over HTTP (`scripts/bench-latency.sh`, N=15) — the user-perceived number

| query | p50 | p95 | max |
|---|---|---|---|
| `func` (common token) | 1.18s | 1.32s | 1.32s |
| `embedding ONNX runtime` | 1.75s | 1.92s | 1.92s |
| `RankConfig` (selective) | 2.19s | 2.28s | 2.28s |
| `intersect posting list` | 2.38s | 2.52s | 2.52s |
| `token budget context assembly` | 2.41s | 2.50s | 2.50s |
| `SetEnclosingBytes` (rare) | 3.03s | 3.14s | 3.14s |
| **ALL** | **2.28s** | **3.08s** | **3.14s** |

### In-process `search_context` (`BenchmarkSearchContext_Real`, 12x)

| query | ns/op | B/op | allocs/op |
|---|---|---|---|
| selective_ident | 2.16s | 1.58 GB | 20.9M |
| common_token | 1.22s | 1.18 GB | 19.1M |
| multiword | 2.52s | 1.82 GB | 21.9M |
| natural_phrase | 2.54s | 1.64 GB | 21.2M |
| rare_symbol | 3.16s | 1.88 GB | 22.3M |

> **~1.4–1.9 GB allocated and ~20M allocations per single query.**

### Per-arm decomposition (`BenchmarkRankArms`, `Rank` only, p50-ish ns/op)

| query | Lexical | LexPath | LexSymbol | Full | path Δ | symbol Δ |
|---|---|---|---|---|---|---|
| selective_ident | 2.00s | 2.00s | 2.18s | 2.18s | ~0 | +0.18s |
| common_token | 1.05s | 1.05s | 1.24s | 1.24s | ~0 | +0.19s |
| multiword | 2.29s | 2.25s | 2.46s | 2.45s | ~0 | +0.20s |
| natural_phrase | 2.31s | 2.32s | 2.48s | 2.46s | ~0 | +0.15s |
| rare_symbol | 2.93s | 2.95s | 3.14s | 3.10s | ~0 | +0.16s |

- **path arm ≈ free** — it uses an inverted `pathPostings` index (the model to copy).
- **symbol arm ≈ +150–200ms** — an O(corpus) scan; real, but small next to the base cost.
- **the ~2s base lives in the lexical path** (which includes `lexicalSpans`, run for every candidate).

### Context assembly (`BenchmarkContextwin_Assemble`)

0.17–0.67 ms/op. **Negligible** (<0.03% of a query). Not a suspect.

## Root cause — `lexicalSpans` (internal/rank/ranker.go:755)

CPU profile: `lexicalSpans` is **50.7% cumulative** (`strings.ToLower` 26%,
`strings.Contains`/`Index` 22%, `strings.Split` 5%). Alloc profile: **426 GB of
481 GB total (88.7%)** — `bytealg.MakeNoZero` 150 GB (the ToLower/Split backing
arrays), `strings.genSplit` 103 GB. Most of the *remaining* CPU is GC
(`gcDrain` 29.9%, `madvise` 9.4%) draining that garbage.

```go
// internal/rank/ranker.go:755 — called once PER CANDIDATE BLOB from Rank (line 356)
func (r *Ranker) lexicalSpans(b *index.Blob, terms []string) []LineSpan {
	lines := strings.Split(string(b.Content), "\n")  // alloc whole content + []string, per blob
	for i, line := range lines {
		low := strings.ToLower(line)                 // alloc a lowercased copy of EVERY line
		for _, t := range terms {
			if t != "" && strings.Contains(low, t) { ... }
		}
	}
}
```

The structural flaw is in `Rank` (ranker.go:353–375): it builds a `RankedResult`
**with spans for every candidate**, *then* sorts and truncates to `topK`. For a
common query term the candidate set is tens of thousands of blobs, so we
lowercase+split tens of thousands of whole files and discard the spans for all
but 12.

## Fix plan (prioritized; all parity-safe — none change ranking order)

1. **Defer `lexicalSpans` to after sort+truncate.** Compute spans only for the
   ≤`topK` results that survive. ~1000× fewer calls on common queries; removes
   ~88% of allocations and most GC. Ranking order is unchanged (scores come from
   `fuse()`, not from spans). **Expected: ~2s → low-hundreds-of-ms.** _Biggest win._
2. **Symbol-name inverted index.** Give `symbolArm` what `pathArm` already has: a
   term→blobs postings map built at load, with symbol subtokens tokenized once at
   build time instead of re-tokenized per query. Removes the O(corpus) scan
   (~150–200ms + 40 GB allocs/query). Becomes the top cost once #1 lands.
3. **`lexicalSpans` micro-opts** (only matters for the ≤topK survivors after #1):
   scan `b.Content` by line via `bytes.IndexByte` without `strings.Split`, and do
   case-insensitive matching without a per-line `ToLower` allocation. Polish.

Out of scope here but noted: the **dense arm is off**, so any "semantic" recall
the design intends is not active in the live daemon.
