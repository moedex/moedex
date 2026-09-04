# ADR 0025: mmap the BM25 and dense sidecars, not just postings

- **Status:** Accepted
- **Date:** 2026-09-04
- **Context owner:** moedex (TurnCommerce)

## Context
ADR 0005 moved positional postings off the heap and named what it did not fix:
*"This fixes serving memory, not build memory: constructing the in-RAM index
(and especially the corpus-wide sidecars) is still the binding constraint."*
On the live 492-shard corpus (56,978 blobs, 17.6M distinct terms, 40.6M postings,
953,451 dense chunks) the sidecars were the whole constraint. A probe attributing
`serve.OpenRank`'s live heap found the BM25 token index at 4,678 MB against 372 MB
on disk — a 12.58x inflation, because `postings` was a `map[string]map[uint64]int`
over 17.6M terms averaging 2.3 postings each, so nearly every term paid a full Go
map header plus a bucket allocation. The dense store held 2,866 MB as 953,451
separate 3,072-byte slice allocations (768-dimensional float32 vectors, 768 × 4
bytes each), holding vectors already written to disk as one contiguous block.
Together with blob content, `lineStarts`, and the symbol index, live heap
totaled 8,748 MB.

## Decision
Make the on-disk format the memory layout for both sidecars, the same discipline
ADR 0005 applied to postings, and mmap them so the loader aliases bytes instead
of parsing them.

- **Token index (TKI1 → TKI2).** Six fixed-width CSR sections instead of nested
  maps: `docLen` (dense, indexed by blob ID), `termOff`/`termText` (terms sorted
  lexicographically, offsets into the concatenated text), `postOff`/`postBlob`/`postTF`
  (per-term posting lists, blob-ascending). `Build` allocates these arrays on the
  heap; `Load` aliases them over a mapping via `internal/mmapslice`. Same struct,
  same accessors, plus one new one: `Postings(term) PostingList`, a resolve-once
  handle that lets `rank.lexicalArm`'s BM25 loop walk sorted candidates against a
  sorted posting list with a galloping cursor instead of a per-`(blob, term)`
  binary search.
- **Dense store (MDXE v1 → v3).** Header gains a `quant uint8` field (0 = float32,
  1 = int8 + a per-vector float32 scale) and explicit section offsets, with the
  vector block padded to a 64-byte boundary so `mmapslice.Float32s` can alias it.
  `vecAt(i)` slices the aliased block instead of indexing a `[]([]float32)`. The
  per-query dimension-validation loop is deleted: a flat block makes a
  mixed-dimension store structurally impossible.
- **`internal/mmapslice`** is the one place `unsafe` lives for both sidecars:
  `Float32s`/`Uint32s`/`Uint64s` alias a byte slice with alignment and
  little-endian checks, fuzz-tested against truncated and corrupt input.
- Both formats bump their magic/version (`TKI2`, `MDXE` v3) rather than adding a
  migration path. `Load` returns `ErrLegacyFormat` on the old versions; every call
  site already treats a load failure as a cache miss and rebuilds, so no converter
  exists or is needed.
- int8 quantization ships as **opt-in**, not the default — see Evidence below for why.

## Consequences

**Positive**
- Live heap on the reference corpus: **8,748 MB → ~1,255 MB**, with the token
  index and dense store each contributing near zero (token index measured
  *−1.6 MB*, below GC noise).
- Token index heap/file ratio: **12.58 → −0.0026**. Dense store: **1.0 → 0.0180**
  against a 2,974,767,232-byte file, with process max RSS while loading it at
  111 MB — proof the block is aliased, not copied.
- The rewritten builder is incidentally faster and lighter: **37.0s / 7,223 MB
  total peak heap / 7.91 GB max RSS**, against the original map-of-maps builder's
  56s / 11,281 MB / 12.86 GB — 1.5x faster and 38% less RSS — because fixing the
  real bottleneck (see refutation 2 below) also fixed wall time.
- Ranking is unaffected: `internal/eval`'s `TestFixtureMeasurement` produces
  byte-identical recall/precision/MRR/NDCG/uDCG at the merge-base and at head
  across all 34 gold queries; `TestCorpusGoldGate` passes.
- Correctness at scale: 49,078 real terms covering 6,535,444 postings verified
  byte-identical between the heap-built and mmap-loaded index; the full TKI2 file
  is byte-identical across builders (sha256 `47c3446fbd0b531bfd8a…`, 655,753,020
  bytes). Fuzzing found 0 crashers over 103.2M (`tokenindex`) + 23.5M (`embed`)
  executions — the repo's first fuzz targets.

**Negative / costs**
- Disk grows to buy addressability: TKI2 is 656 MB against TKI1's 372 MB on the
  reference corpus. Fixed-width CSR costs disk; the trade is what makes the file
  mmap-addressable without decoding 40.6M varints onto the heap.
- Build's total peak heap is **7,223 MB**, of which ~1,579 MB is corpus blob
  content already resident before `Build` runs — so `Build`'s own contribution
  is **5,644 MB**, not the ~1.7 GB this spec projected. See refutation 1 for the
  projection comparison. GC headroom, not the algorithm, is now the dominant
  lever on that number (see Evidence).
- int8 quantization does not deliver its projected speedup — see refutation 3 —
  so it stays opt-in rather than solving the still-open 2.93 GB mapped-but-touched
  cost of a full f32 dense scan.
- 1,255 MB of live heap remains out of scope: blob content (866 MB), `lineStarts`
  (254 MB), and the symbol index (84 MB) are unchanged by this work.

## Evidence

Serving heap, real corpus (56,978 blobs, 17.6M distinct terms, 40.6M postings,
953,451 dense chunks):

| component | before | after |
|---|---:|---:|
| BM25 token index | 4,678 MB | ~0 MB (measured −1.6 MB, below GC noise) |
| dense embedding store | 2,866 MB | 50.9 MB |
| blob content | 866 MB | 866 MB (out of scope) |
| `lineStarts` | 254 MB | 254 MB (out of scope) |
| symbol index | 84 MB | 84 MB (out of scope) |
| **total live heap** | **8,748 MB** | **~1,255 MB** |

Index build, same corpus, output byte-identical in every configuration. "Peak
heap" below is **total** process peak heap, which includes ~1,579 MB of corpus
blob content already resident before `Build` is called — `Build`'s own
contribution above that baseline is smaller and is what the spec's ~1.7 GB
projection was actually estimating (see refutation 1):

| builder | wall | peak heap (total) | max RSS |
|---|---:|---:|---:|
| original map-of-maps | 56s | 11,281 MB | 12.86 GB |
| sort-based (mid-project) | 4m23s | 7,544 MB | 8.78 GB |
| final (dense per-blob scratch) | 37.0s | 7,223 MB | 7.91 GB |

int8 quantization (opt-in, not the default), measured over 40 real queries drawn
from the corpus: file 2,975 MB → 782 MB (3.80x smaller), recall@10 0.9500,
rank-1 agreement 1.0000, mean scan 37 ms (f32) vs 38 ms (int8) — 0.97x, no
speedup.

**Three refutations, recorded because this repo's ADRs keep what measurement
disproved, not just what it confirmed. All three were projections in the spec
that this project's own measurement overturned:**

1. *The spec projected a build peak of ~1.7 GB — measuring `Build`'s own
   contribution above the resident corpus baseline, not total process heap.
   That contribution came in at 5,965 MB on the sort-based builder where this
   was first measured — 3.5x off — and still 5,644 MB (3.3x off) on the final,
   shipped builder.* The arithmetic omitted the CSR output arrays coexisting
   with the build inputs, append-doubling transients, and underestimated the
   17.6M-entry dictionary.
2. *The record sort was never the bottleneck.* A counting scatter replacing the
   40.6M-record comparison sort measured neutral. A CPU profile then showed the
   real cause: `Build` reused one `perBlob` map across blobs, and a Go map never
   returns capacity — `clear` keeps the Swiss table
   ([golang/go#70617](https://github.com/golang/go/issues/70617), still open) and
   `range` walks the whole table. One blob holds 11,018,072 distinct terms — 63%
   of the corpus vocabulary — sizing that table to ~16.7M slots permanently, so
   all 56,977 other blobs paid a full-table walk. `range perBlob` was 172.68s of
   232.51s profiled samples (74.3%); the sort was 1.88%.
3. *int8 does not make the scan faster.* The spec projected ~4x on the reasoning
   that the scan is memory-bandwidth-bound. Measured: 0.97x. `scoreAgainst`'s int8
   path converts each int8 to float32 per element, doing the same FP work as f32
   plus a conversion. A genuinely faster path needs an integer dot product against
   a quantized query — a different design that would compound quantization error.

**Finding, not a defect:** the dominant lever on build memory is GC headroom, not
the algorithm. `GOGC=25` alone takes build peak to 4,974 MB and max RSS to
6.39 GB, at about 12% more wall time. This stays out of scope for code — a
library must not set global GC policy — but an operator should know it is
available.

## Related
[0005](./0005-mmap-compact-postings.md) (the precedent; its own Negative section
named these sidecars as the remaining binding constraint — this ADR closes that
gap), [0001](./0001-single-node-scope-pure-go-default.md) (single-node envelope),
[0006](./0006-rrf-hybrid-ranking.md) (the arms that consume these sidecars),
[0007](./0007-optional-dense-arm.md) (the dense arm's build-tag boundary),
[0014](./0014-eval-harness-gold-gate.md) (the gate that pins ranking parity).
