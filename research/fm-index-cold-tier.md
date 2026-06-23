# FM-Index as a moedex Cold/Archival Tier — Implementation Guidance

> Research synthesis, 2026-06-22. Grounds the northstar's "Compressed self-indexes
> (FM-index)" ADD item against moedex's actual post-slice-4 baseline.
> Sources with URLs + dates at the bottom. The refuted "44% of corpus" framing is
> corrected explicitly in §1.

---

## Verdict (read this first)

**Not warranted at moedex's current scale. Do nothing now except track one metric.**

An FM-index is a genuinely good fit for a *cold/archival* exact-match tier at
**internet scale** (tens of TB), where storing positional trigram postings is
ruinous. moedex is a single-node ~8GB corpus that, after slices 3+4, sits at
**~1.66x content in RAM** and **~2.8x content on disk** with mmap'd compact
postings. At that size there is no cold-tier problem to solve: the hot trigram
core *is* the whole index and it fits comfortably in RAM. Adding an FM-index now
would buy nothing and cost a C++/SDSL cgo dependency that breaks moedex's
pure-stdlib, `go get`-able posture.

The FM-index also **cannot replace the trigram core** — it does exact
substring/n-gram match only, not regex. moedex's correctness model is
regex→trigram necessary-condition reduction + verify, validated by ripgrep
parity. An FM-index can at best be an *alternative retrieval backend for the same
candidate-extraction step* (literals/n-grams → FM-index locate → verify), and
even then it competes with a trigram index that is already cheap and correct.

**The crossover metric to watch:** the on-disk size of the positional trigram
index relative to available NVMe/RAM budget on the single node. Concretely:
**when the mmap'd index (currently ~2.8x content on disk, residual ~0.66x content
resident in RAM) stops fitting the hot node's RAM+SSD budget for the corpus you
want live** — i.e. when corpus × 2.8 exceeds disk, or corpus × ~1.66 exceeds RAM
you're willing to dedicate — that is when a compressed cold tier earns its
complexity. For a single node that's realistically corpus growth into the
**hundreds of GB to low TB**, not 8GB. Revisit then.

---

## 1. What an FM-index actually buys, honestly (and the "44%" correction)

An FM-index is a *compressed self-index*: it stores the Burrows-Wheeler
Transform of the text plus a sampled suffix array and rank structures (typically
a wavelet tree), and it answers exact-substring queries directly over that
compressed form — **no separate positional postings, and the original text is
recoverable from the index** (hence "self-index"). Infini-gram mini (Xu, Liu,
Choi, Smith, Hajishirzi; EMNLP 2025, arXiv:2506.12229) uses exactly this to make
petabyte-class corpora searchable, indexing **83 TB of text in 99 days on one
128-vCPU node** (19 hours on 137 nodes), with **18x faster indexing and 3.2x
lower indexing memory** than the prior best FM-index implementation.

**The "44% of corpus" claim — corrected.** The paper does state its index is
"size only 44% of the corpus" and "down to 7% compared to a canonical suffix
array index." Both numbers are real *as reported*. What the northstar's
deep-research run **refuted (scored 0-3)** is the stronger inference that *an
FM-index is therefore strictly smaller than a trigram index*. That does not
follow, for three reasons:

1. **44% is FM-index vs. raw text, not vs. trigrams.** Cox's *presence-only*
   trigram index is ~20% of file size; moedex's *positional* index is ~2.8x
   content on disk but the genuinely comparable figure is the resident RAM
   footprint, ~1.66x content. The 44%/raw-text figure is measured against a
   different baseline and answers a different question.
2. **Different query power.** The 44% buys *exact substring/n-gram match only*.
   The 2.8x buys *regex via trigram reduction + positional verification*. You are
   not comparing like for like; the FM-index is cheaper partly because it does
   less.
3. **Corpus-dependent.** Compression ratio depends on text entropy; source code
   (repetitive, low-entropy) compresses well, but so do delta-coded trigram
   postings (slice 3's grouped-varint codec already gets ~16 bytes/posting down
   to ~1-2 bytes). The headline ratio is not a portable constant.

So: treat FM-index as **"compact exact-match self-index, competitive on size at
huge scale,"** never as **"a strictly smaller drop-in for trigrams."**

**Query cost — the count/locate asymmetry that matters for code search.**

- **Count / exists** (`how many times does this n-gram appear`) is the FM-index's
  sweet spot: *backward search* is O(m) in the pattern length m and
  **independent of corpus size n** — Infini-gram mini counts occurrences of
  arbitrarily long strings in 45.6 TB "in seconds." This is strictly better than
  trigram+verify for pure count/exists workloads.
- **Locate** (return the actual positions / enclosing documents) is **markedly
  more expensive** and is the operation code search actually needs. Each reported
  occurrence requires walking the sampled suffix array back to a sampled position
  — cost proportional to the suffix-array sample spacing per hit, times the number
  of hits. For a literal that occurs thousands of times across a corpus (common in
  code: `func`, `return`, `import`), locate cost scales with the match count and
  the sampling rate, which is exactly the regime where trigram intersection +
  positional verify is already fast and where you'd pay the FM-index's locate tax
  on every hit. (This count-cheap / locate-expensive asymmetry is a structural
  property of FM-indexes, not a moedex-specific observation.)

**Where FM-index genuinely wins:** (a) corpora too large for positional postings
to fit budget; (b) count/exists/"does this string appear anywhere" queries
(contamination analysis, dedup, license/secret scanning); (c) archival tiers
queried rarely, where the self-index doubling as compressed storage is a feature.
moedex has none of these as a pressing need at 8GB.

---

## 2. Does it fit moedex's regex / ripgrep-parity story?

**Only as a candidate-retrieval backend, never as the correctness model — and
even then it's a sidegrade.**

- **FM-index is exact-substring only.** No regex. Confirmed: Infini-gram mini and
  FM-indexes generally support count / find-positions / document-retrieval over
  literal n-grams, not regular-expression matching.
- moedex's parity guarantee comes from **regex → boolean trigram query
  (necessary condition) → fetch candidates → verify with the real regex engine.**
  The verify step is what makes results identical to ripgrep. An FM-index changes
  *only the candidate-fetch step*: you would still do Cox-style subexpression
  analysis to extract required literals/n-grams, but instead of intersecting
  trigram posting lists you'd issue FM-index `locate` calls for each required
  literal and intersect the resulting document/position sets, then run the **same
  verify pass**. Parity is preserved **as long as verify is unchanged** — the
  retrieval backend is, by design, allowed to over-return candidates.
- **But the substitution is a sidegrade at moedex's scale, for two reasons.**
  (1) The extracted-literal sets from regex reduction are often *trigrams* (Cox's
  whole point is 3 is the sweet spot); FM-index `locate` on 3-byte strings returns
  enormous occurrence sets, and you pay the per-hit locate tax on all of them —
  precisely the workload where positional trigram intersection is cheapest.
  (2) You'd run **two indexes** (FM-index can't do regex-only queries that have no
  extractable literal, e.g. `/a.c/` style classes), so the trigram core stays
  regardless; the FM-index becomes pure added surface area.

**Integration shape that preserves parity (if ever built):** keep the trigram
core as the hot/regex backend; expose a `CandidateSource` interface that the
query planner can satisfy from either trigram postings (hot tier) or FM-index
locate (cold tier), selected per-shard by tier. The verify pass is shared and
authoritative. The ripgrep-parity test suite (`internal/search/parity_test.go`,
`mmap_parity_test.go`) must run green against *both* backends — that is the gate.
This is the right abstraction to *note now and not build*.

---

## 3. Go implementation reality

**This is cgo-to-C++ territory. Pure-Go is a research project, not a slice.**

- **Infini-gram mini is C++ (~97%) + SDSL, with pybind11 Python bindings.** No Go,
  no cgo path. Reusing it from moedex means either (a) cgo bindings over its C++
  (dragging in SDSL, a heavyweight succinct-data-structure library, plus a fussy
  toolchain — its indexing path wants a specific conda/GCC env), or (b) running it
  as an out-of-process service and talking to it over IPC/HTTP. Option (b) is the
  only one that doesn't poison moedex's pure-stdlib, statically-linked,
  `go get`-able posture — and it's a whole separate subsystem to operate.
- **Pure-Go FM-index ecosystem is sparse and stale (2026).** The notable Go repos
  — a BWT/FM-index lib (bioinformatics-oriented, ~16 stars, last touched ~Apr
  2023) and a separate wavelet-tree repo (~Mar 2023) — are unmaintained and
  unproven at scale. The actively maintained succinct-structure work lives in C++
  (SDSL, BWTIL, DYNAMIC) and increasingly Rust (an FM-index crate updated Apr
  2025). Nothing in Go is production-grade.
- **Effort to do it right in pure Go:** you'd be implementing, from stdlib, a
  BWT construction (SA-IS or DC3 suffix-array construction over bytes), a
  rank/select bitvector, a wavelet tree over the BWT, suffix-array sampling, and
  backward-search + locate. That's a multi-week-to-months succinct-structures
  effort with subtle correctness and performance traps (cache-efficient rank,
  sampling-rate/space tradeoffs). It is *feasible* in pure Go and would fit
  moedex's no-dep ethos, but it is a large, specialized build justified only once
  the crossover threshold (§4) is actually hit. The byte-trigram switch in slice 4
  is convenient here: an FM-index over bytes aligns with moedex's already
  byte-oriented, UTF-8-exact verification model.

**Recommendation:** do not take the cgo dependency now. If a cold tier ever
materializes, prototype with the out-of-process Infini-gram mini service to
validate the workload, and only then decide whether a pure-Go FM-index is worth
building to keep the dependency story clean.

---

## 4. Tiering architecture & crossover threshold

**Hot/cold split (the shape, for when it's needed):**

- **Hot tier — positional byte-trigram index (today's engine).** Recent/active
  repos, anything queried with regex, anything needing positional verification and
  ripgrep parity. Stays in RAM/NVMe at ~1.66x / ~2.8x content. This is the
  authoritative, full-power tier.
- **Cold tier — FM-index self-index.** Archival/inactive repos, queried rarely,
  exact-literal/count workloads acceptable (or literal-extraction → locate →
  verify with reduced expectations). The self-index doubles as compressed storage
  of the blobs, so the cold tier can drop separate content storage.
- **Content-addressing by blob SHA is the clean seam.** moedex keys blobs by git
  blob SHA (content-addressed, deduped). A blob's *tier* is just metadata on the
  SHA: hot blobs live in the trigram index, cold blobs in the FM-index, and the
  query planner unions candidates across tiers before the shared verify pass.
  Promotion/demotion between tiers is "re-index this SHA into the other
  structure" — no identity churn, no dedup loss, because identity is the SHA, not
  the tier. This is the part of the northstar's content-addressable architecture
  that makes tiering cheap to reason about.

**When does a single-node ~8GB project cross into needing this?**

Never at 8GB. The crossover is purely a budget question on the hot node:

- **Disk crossover:** when `corpus_bytes × ~2.8` exceeds the NVMe you'll dedicate
  to the index. At 8GB that's ~22GB — trivial. This bites in the **hundreds of
  GB** range on a modest single disk.
- **RAM crossover (the real one):** the mmap loader keeps content + line-start
  tables + trigram directory resident (~1.66x content; the ~0.66x residual is
  metadata, postings are decoded on-demand from the mapping). When `corpus_bytes ×
  ~1.66` exceeds the RAM you'll dedicate, query-time paging thrashes. At 8GB that's
  ~13GB resident — fine. This bites somewhere in the **tens-to-low-hundreds of GB
  of live corpus per node**, depending on the box.

So the trigger is not a date or a feature, it's: **live corpus on one node grows
~1-2 orders of magnitude (toward low-TB), AND a meaningful fraction of it is cold
(rarely queried, exact-match-acceptable).** Until both hold, the FM-index is
strictly added complexity.

---

## 5. What to do now

1. **Build nothing.** No FM-index, no cgo, no Infini-gram mini integration.
2. **Add a tracking note** (this doc) and **watch one metric:** resident index RAM
   as a multiple of content (today ~1.66x) and on-disk index size (~2.8x) versus
   the single node's RAM/SSD budget. Surface it wherever index build stats are
   already reported.
3. **Preserve the seam cheaply when you next touch the query planner:** if a
   `CandidateSource` / retrieval-backend abstraction is natural to introduce
   anyway (it also helps the hybrid-ranking work the northstar prioritizes), shape
   it so a future cold-tier backend could plug in behind the shared verify pass.
   Don't build the backend; just don't paint yourself out of it.
4. **Revisit when** live single-node corpus approaches the RAM crossover (§4) *and*
   a cold subset exists. At that point: prototype against the out-of-process
   Infini-gram mini service first; only build a pure-Go FM-index if the dependency
   story demands it.

---

## Sources

- **Infini-gram mini: Exact n-gram Search at the Internet Scale with FM-Index**,
  Xu, Liu, Choi, Smith, Hajishirzi — EMNLP 2025, arXiv:2506.12229 (submitted
  2025-06-13, v5 2026-01-06).
  https://arxiv.org/abs/2506.12229 / https://aclanthology.org/2025.emnlp-main.1268/
  - Claims: index "size only 44% of the corpus"; "down to 7% compared to a
    canonical suffix array index"; "18x" faster indexing, "3.2x" lower indexing
    memory vs prior best FM-index; "83TB ... in 99 days with a single ... 128
    vCPUs (or 19 hours if using 137 such nodes)"; counts in "45.6 TB" of text
    "in seconds"; FM-index "simultaneously indexes and compresses text." Search
    time complexity "independent of n."
- **Infini-gram mini source** — https://github.com/xuhaoxh/infini-gram-mini
  (accessed 2026-06-22). C++ ~97% + SDSL, pybind11 Python bindings; no Go/cgo;
  build needs C++17 + SDSL + specific GCC/conda env. Operations: count, find
  positions, document retrieval. No regex.
- **Ferragina & Manzini, "Opportunistic Data Structures with Applications"**
  (FOCS 2000) — original FM-index ("Full-text index in Minute space"); BWT +
  sampled suffix array + rank; count via backward search, locate via SA sampling.
- **Russ Cox, "Regular Expression Matching with a Trigram Index"** (swtch.com) —
  presence-only trigram index ~20% of file size; regex→trigram reduction. Basis
  for moedex's correctness model. https://swtch.com/~rsc/regexp/regexp4.html
- **Go ecosystem** (GitHub topics `fm-index`, `wavelet-tree`, accessed
  2026-06-22): pure-Go BWT/FM-index and wavelet-tree repos exist but are
  small/unmaintained (last updates ~2023); maintained succinct-structure work is
  C++ (SDSL, BWTIL, DYNAMIC) and Rust (FM-index crate, ~Apr 2025).
- **moedex baseline** — slice 3 (`moedex-slice3.md`) and slice 4
  (`moedex-slice4.md`): post-slice-4 RAM ~1.66x content (114 MB on 68.7 MB
  corpus), on-disk index ~2.8x content (192 MB), grouped-varint postings codec
  (`internal/index/codec.go`), mmap loader (`internal/diskstore`), ripgrep parity
  (`internal/search/parity_test.go`, `mmap_parity_test.go`).
- **northstar** — `zoekt-2026-redesign.md`: FM-index listed as ADD for
  cold/archival tier; "FM-index is only 44% of corpus" refuted **0-3** (don't sell
  as strictly smaller than trigrams).
```
