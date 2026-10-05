# ADR 0012: Search latency — fold-aware candidate prefilter + positional-postings verification for the `(?i)` tail

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
The parity gate ([0003](./0003-cox-reduction-ripgrep-parity.md)) proved moedex *correct* but exposed a brutal latency tail. The seeded 1000-query battery on the 953.6 MB corpus measured p50 **10.4 ms** but **p95 15.3 s** (max 39.0 s). The tail was dominated by case-insensitive queries (bucket `h`): the fold-safe fix for the parity bug forced every `(?i)` literal to degrade to `match: All` and then re-scan content with RE2 — a `(?i)` query matching 5 lines took 11 s while its case-sensitive twin took ~5.7 ms, a ~2000× gap. Sub-trigram literals and weak regex (anchors, broad classes) had the same disease: a correct-but-`All` candidate set followed by an O(corpus) content scan.

## Decision
Close the gap with two co-dependent techniques plus intra-query parallelism, all preserving the never-under-approximate invariant ([0003](./0003-cox-reduction-ripgrep-parity.md)):

1. **Fold-aware candidate trigrams** (`internal/query/cox.go`): enumerate a folded literal's case-fold trigram closure via `unicode.SimpleFold` (which matches Go's `(?i)` simple-fold exactly), emitting AND-over-positions of OR-over-fold-variants — turning `(?i)` from `All` into a real constraint. On cap overflow, fall back to `All` (sound).
2. **Positional-postings verification** (`internal/search`, `regexPositional`): when the filter reduces to trigram-length literals, derive candidate **lines directly from `ix.Postings(trigram)`** — pick the most-selective driver position via a cheap `Index.PostingCount` varint count-walk ([0005](./0005-mmap-compact-postings.md)), map each posting offset to its line via `Blob.LineAt`, dedup, then run RE2 only on those lines. This is the same fast path that keeps case-*sensitive* rare literals at ~5 ms — now extended to `(?i)`. A `maxPositionalPostings` cap falls back to a bounded content scan when the driver trigram is too common.
3. **Intra-query parallelism**: fan the candidate/blob loops across `NumCPU` workers (work-stealing on a shared cursor, because minified lines cluster in posting order).

**Rejected:** a multi-position folded prefilter (P4 "two-filters") — measured no win, a slight regression, because the cost was bytes scanned over a huge candidate-line set, not filter selectivity. A buffer-level `bytes.Index` prefilter — same, no win.

## Consequences
**Positive**
- The `(?i)` tail collapses from re-scanning content to jumping to exact offsets; serving latency on the worst surviving shapes drops from ~7–11 s to ~350–650 ms (a ~15–20× single-query win).

**Negative / costs**
- A residual tail is **inherent**, not a bug, and is explicitly parked: high-match scans that are O(corpus) regardless of indexing (`\p{Greek}` 12.7 s, `[0-9]{4}`, sub-trigram `;`), and **fold-dirty `k`/`s` literals** where no ASCII-safe trigram exists so a content scan is *required* for fold-soundness. A per-request deadline ([0010](./0010-warm-serving-spine.md)) is the pragmatic cap for these.
- A native SIMD verify kernel could shave the inherent scans, but profiling shows the tail is scan-bound, not intersection-bound — deferred ([0013](./0013-pure-go-defer-simd.md)).

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0002](./0002-positional-trigram-core-byte-offsets.md), [0003](./0003-cox-reduction-ripgrep-parity.md), [0005](./0005-mmap-compact-postings.md), [0013](./0013-pure-go-defer-simd.md).
