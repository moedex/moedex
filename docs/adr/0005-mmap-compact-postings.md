# ADR 0005: mmap'd compact (varint-delta) postings as the memory strategy

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
Positional postings ([0002](./0002-positional-trigram-core-byte-offsets.md)) are the memory wall: roughly one `Posting` per content byte, ~16 bytes each in their naive in-RAM form. Holding the whole positional index of a multi-GB corpus on the Go heap is the thing that breaks the single-node envelope ([0001](./0001-single-node-scope-pure-go-default.md)) — and GC over a heap that large is its own tax. The research note observed positional trigrams need only ~1.2× corpus in RAM *with posting lists on SSD* — which only holds if the postings actually stay off-heap.

## Decision
Persist the index to a single file with **grouped varint delta-coded** posting lists, and serve queries from an **mmap'd** file so postings never enter the Go heap.

- `internal/index/codec.go` encodes each trigram's list per blob as (blob-id delta, count, then offset deltas) — typically 1–2 bytes per posting.
- Each list is written as an **individually-addressable byte range** (`MOEDEX03`, 48-byte header + blob section + postings section), so `diskstore.LoadMmap` maps the whole file and hands the index self-contained byte sub-slices per trigram.
- A query decodes only the trigrams it touches; the bulk of postings are read straight from the mapping by the OS page cache. `cmd/scale` measures this directly (`MOEDEX_MMAP=1`).

## Consequences
**Positive**
- The serving working set is the OS page cache over the mapping, not the Go heap — RAM is reclaimable under pressure and GC has almost nothing to scan.
- Delta coding plus dedup ([0004](./0004-content-addressable-blob-store.md)) keeps the on-disk index small enough to fit the single-node disk envelope.
- The byte-range layout means `PostingCount` can be answered by a cheap varint count-walk without decoding the list — directly exploited by the latency path ([0012](./0012-search-latency-positional-verify.md)).

**Negative / costs**
- This fixes *serving* memory, not *build* memory: constructing the in-RAM index (and especially the corpus-wide sidecars) is still the binding constraint ([0001](./0001-single-node-scope-pure-go-default.md)).
- mmap ties the working set to OS page-cache behavior — fine on a dedicated box, less predictable under memory contention with other tenants (not a concern given the single-node, internal-first scope).

## Evidence
Scale run on the 5.2 GB / 484-repo corpus (`cmd/scale`, `MOEDEX_MMAP=1`, 748,144,248 postings): the in-RAM positional index holds **15.41× content** on the heap (≈14,694 MB); after persist → drop in-RAM copy → reload with postings mmap'd, heap drops to **1.12× content** (≈1,069 MB) — a **13.75× heap reduction**, proving the working set is the mapping, not the heap. Warm grep p95 stayed **563 ms** on the 5.2 GB corpus.

## Related
[0001](./0001-single-node-scope-pure-go-default.md), [0002](./0002-positional-trigram-core-byte-offsets.md), [0004](./0004-content-addressable-blob-store.md), [0012](./0012-search-latency-positional-verify.md).
