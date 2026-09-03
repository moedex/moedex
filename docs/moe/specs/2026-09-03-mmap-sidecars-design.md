# Move the BM25 and dense sidecars off the Go heap

- **Status:** Approved, not implemented. 0 of 2 phases landed. Measurements below are real (probe against the live 492-shard corpus, 2026-09-03); every projected figure is labelled as a projection.
- **Date:** 2026-09-03
- **Depth:** feature (new on-disk formats, new resource lifecycle, builder rewrite)
- **Extends:** [ADR 0005](../../adr/0005-mmap-compact-postings.md)

Take the two corpus sidecars off the heap and serve them from a mapping, the way
postings already are. Live heap drops from 8.75 GB to about 1.2 GB.

## The problem, measured

ADR 0005 moved positional postings off the heap and named what it did not fix:
*"This fixes serving memory, not build memory: constructing the in-RAM index
(and especially the corpus-wide sidecars) is still the binding constraint."*
The sidecars are now the whole constraint. A probe that loads exactly what
`serve.OpenRank` loads, against the live corpus, attributes the heap:

| Component | Live heap | On disk | Inflation |
|---|---:|---:|---:|
| BM25 token index | **4,678 MB** | 372 MB | **12.6x** |
| Dense embedding store | **2,866 MB** | 2,975 MB | 1.0x |
| Blob content | 866 MB | 820 MB | 1.1x |
| `lineStarts` arrays | 254 MB | — | — |
| Symbol index | 84 MB | 16 MB | 5.3x |
| **Total live heap** | **8,748 MB** | | |

The daemon runs at 12.7 GB RSS. The remainder is GC headroom, ONNX arenas, and
touched pages of the 758 MB mapped graph.

**The token index is the outlier, and the cause is structural.** `postings` is a
`map[string]map[uint64]int` over 17,608,556 terms and 40,585,445 postings. Average
document frequency is **2.3**, so nearly every term pays a full Go map header plus
a bucket allocation to hold two entries. That is 115 bytes per posting, and roughly
3.5 GB of pure map overhead.

The dense store's `vectors` is a slice of slices: 953,451 separate 3,072-byte
allocations plus 23 MB of slice headers, holding 768-dimensional unit vectors that
are already written to disk as one contiguous block.

## Decision

Make the on-disk format the memory layout. Both sidecars become fixed-width,
little-endian, mmap-addressable structures that a loader maps rather than parses.

This is ADR 0005's discipline applied to the two sidecars it explicitly excluded.
Phase 1 is the token index; phase 2 is the dense store. Ship them in that order.
Neither is done until both are done.

## Phase 1 — Token index (TKI2)

### Format

Header carries an explicit offset and length per section, so the reader validates
bounds instead of recomputing them. Every section is 8-byte aligned.

```
HEADER    magic "TKI2", version, numDocs, numTerms, numPostings, totalLen,
          then an (offset, length) pair per section below
docLen    []uint32   dense, indexed by blob ID; 0 for gaps
termOff   []uint32   numTerms+1, CSR into termText
termText  []byte     concatenated term text, sorted lexicographically
postOff   []uint32   numTerms+1, CSR into the posting arrays
postBlob  []uint32   ascending within each term
postTF    []uint32   parallel to postBlob
```

Projected size: 656 MB (docLen 0.2, termOff 70.4, termText 190.0, postOff 70.4,
postBlob 162.3, postTF 162.3). Larger than today's 372 MB of varints, because
fixed width is what makes the file addressable without decoding. Disk buys heap.

A posting is **uint32 blob plus uint32 tf, exact, never saturated**. Six-byte
records would save 81 MB of page cache and risk clamping an outlier term
frequency; with the array mapped rather than resident, that trade is not worth a
single changed BM25 score.

`docLen` is dense and indexed by blob ID, with 0 for gaps. That matches the
current `map[uint64]int` semantics exactly, since `DocLen` already returns 0 for
an unknown blob. `numDocs` stays a separate counter and keeps counting non-nil
blobs only.

Save rejects a corpus that overflows the width of any offset field rather than
truncating it.

### One representation, two owners

`Build` allocates the CSR arrays on the heap; `Load` aliases them over a mapping.
Same struct, same accessors, same code paths. `Close` unmaps when mapped and does
nothing when built.

### Accessors

The eight signatures the package doc freezes stay exactly as they are. The change
**adds** one:

```go
func (ti *TokenIndex) Postings(term string) PostingList
```

`PostingList` is a term resolved once — a handle over `postBlob[lo:hi]` and
`postTF[lo:hi]`. `DocFreq`, `TermFreq`, and `Docs` remain, reimplemented on top
of it.

### This makes BM25 faster

`rank.lexicalArm` currently calls `TermFreq(t, blob)` once per `(blob, term)`
pair, and each call re-resolves the term. Under CSR that would mean a binary
search over 17.6 M terms per pair — a real regression if left alone.

Both inputs are already sorted ascending: `candidateBlobs` and
`tokenCandidateBlobs` both sort their output, and CSR posting lists are sorted by
blob. So the arm resolves each distinct term once into a `PostingList`, then walks
the candidates with a galloping cursor per term. The inner loop becomes
**O(|cand| + sum of df) with no per-pair lookup at all**.

`tokenCandidateBlobs` stops allocating and sorting a `[]uint64` per term and
unions sub-slices instead.

### Builder

`Build` peaks at 4.7 GB today, which means a memory-constrained operator cannot
build the index even if serving it would fit. Rewrite it sort-based:

1. Intern terms into `map[string]uint32`.
2. Per blob, count term frequencies in a small reusable `map[uint32]uint32`, then
   flush to a flat `[]struct{termID, blob, tf uint32}` — exactly one record per
   posting.
3. Sort the term dictionary lexicographically and assign IDs in sorted order, so
   sorting records by `(termID, blob)` yields CSR order directly.
4. Stream the sections out.

Projected peak: about 1.7 GB (dictionary ~1.2 GB, records 40.6 M x 12 B = 487 MB).
Not an external merge sort. 1.7 GB once, offline, buys 4.7 GB resident forever;
an on-disk sort buys the difference between those two numbers and costs a whole
subsystem.

## Phase 2 — Dense store (MDXE v3)

The current codec already anticipates this: *"Vectors are stored as a contiguous
block after the chunk metadata so a future loader can mmap/stream them."*

- Header gains `quant uint8` (0 = float32, 1 = int8), explicit section offsets,
  and padding that puts the vector block on a **64-byte boundary**.
- `Store` holds one `[]float32` aliased over the whole block. `vecAt(i)` returns
  `block[i*dim:(i+1)*dim]`. This removes 953,451 allocations and 23 MB of headers.
- **`Search`'s per-query validation loop is deleted.** It iterates all 953,451
  vectors checking `len(v) != s.dim` on every single query. A flat block makes a
  mixed-dimension store structurally impossible, so the check has nothing left to
  find.
- `chunks` and `keys` decode on touch. Only the topK hits are ever read, so
  aliasing them buys nothing and costs alignment constraints.
- **int8 ships as opt-in, not as the default.** Per-vector float32 scale, int32
  dot, rescale. Projected 736 MB mapped (vectors plus a float32 scale each) and roughly a 4x faster scan, at a recall
  cost this spec refuses to guess. `internal/eval` measures NDCG and recall@k on
  the real corpus, and that number decides whether it ever becomes the default.
  The f32 path stays byte-identical to today's scores.

**Hazard to pin with a test.** `RefreshEmbeddings` reuses vectors from the open
store while writing its replacement. `Save` writes a temp sibling and renames, so
the old mapping stays valid for the whole write — but that is now load-bearing
rather than incidental, and a test must say so.

## Shared — `internal/mmapslice`

A 732 M-element cosine scan cannot afford `binary.LittleEndian` per element, so
the dense path needs `unsafe` aliasing. One small package provides it for both
sidecars:

```go
func Float32s(b []byte, n int) ([]float32, error)
func Uint32s(b []byte, n int) ([]uint32, error)
func Uint64s(b []byte, n int) ([]uint64, error)
```

Alignment enforced, length checked, little-endian asserted at init, fuzz-tested.
All `unsafe` in moedex lives in this one reviewable file.

This adds no portability burden: `internal/diskstore` already calls
`syscall.Mmap` with no build tags, and `PLUGIN_TARGETS` is
`darwin/{arm64,amd64} linux/{arm64,amd64}` — all little-endian.

## Lifecycle

`TokenIndex.Close` and `embed.Store.Close` unmap when mapped and no-op when built.
`RankCorpus.Close` gains both, alongside the content store it already releases.

**No new machinery is required.** `rankSnapshot.retire` already drains readers via
its `WaitGroup` and calls `rc.Close()`, logging any failure; `RankCorpus.Close`
already exists precisely because the deduped content store is a mapping. Adding
two more mappings widens a struct and changes no control flow.

`internal/eval` and `internal/app/mcpcmd` call `Build` and hold heap arrays, so
`Close` is a no-op there. They get a `defer Close()` regardless, so the contract
holds at every call site rather than at the ones that currently need it.

## Compatibility

`Load` returns a typed `ErrLegacyFormat` on `TKI1` and on `MDXE` v1 and v2.
`loadPersistedTokens` already returns `(nil, false)` on any load error and lets
`OpenRank` rebuild; the dense sidecar's `.meta` path behaves the same way. So a
format bump needs **no migration code and no legacy reader** — the upgrade costs
one rebuild on first boot.

## Definition of done

Both phases, all five gates:

1. **Memory regression test.** Load a fixture corpus, assert live heap stays under
   a threshold. This is the invariant the work buys, so it gets a test.
2. **Eval parity run.** `internal/eval` NDCG and recall@k identical before and
   after for the f32 path. The int8 delta is quantified and recorded, not waived.
3. **Round-trip and fuzz.** Save/Load identity for TKI2 and MDXE v3, plus fuzzing
   both readers against truncated and corrupt files. An mmap reader that trusts a
   bad offset segfaults where a parser would have returned an error, so the bounds
   checks are the safety property under test.
4. **Before/after benchmarks.** The BM25 hot loop and the dense scan, so the
   galloping cursor and the deleted validation loop are measured rather than
   asserted.
5. **`make verify` green, plus ADR 0025.** Ripgrep parity is untouched — the token
   index is not in the parity path — but the gate is the gate.

## Expected result

| | Now | Projected |
|---|---:|---:|
| Token index | 4,678 MB heap | ~0 heap (656 MB mapped) |
| Dense store | 2,866 MB heap | ~0 heap (2.93 GB mapped f32, 736 MB int8) |
| Blob content, `lineStarts`, symbols | 1,204 MB heap | unchanged |
| **Live heap** | **8,748 MB** | **~1,204 MB** |
| Build peak | 4,678 MB | ~1,700 MB |

Daemon RSS should land near 2 GB of heap and runtime overhead, with the rest as
evictable page cache.

## What this does not fix

Named here so the next reader does not re-derive them:

- **1,204 MB of heap remains**, and this spec does not touch it: blob content
  (866 MB), `lineStarts` (254 MB), the symbol index (84 MB). Blob content is
  already solved by re-exporting the shard dir in the deduped `MOEDEX05` format,
  which needs no code at all. `lineStarts` is eagerly built for every blob and
  rarely read; lazy construction would recover most of 254 MB.
- **Mapped pages are still resident when hot.** A dense query scans every vector,
  so the f32 block is touched in full. The win is that those pages are evictable
  under pressure and invisible to the GC, not that they are free.
- **GC headroom is untouched.** `GOMEMLIMIT` is a separate, zero-code lever.
- **ONNX runtime arenas are untouched.**

## Related

[ADR 0005](../../adr/0005-mmap-compact-postings.md) (the precedent and the gap
this closes), [ADR 0001](../../adr/0001-single-node-scope-pure-go-default.md)
(single-node envelope), [ADR 0006](../../adr/0006-rrf-hybrid-ranking.md) (the
arms that consume these sidecars), [ADR 0007](../../adr/0007-optional-dense-arm.md)
(the dense arm's build-tag boundary).
