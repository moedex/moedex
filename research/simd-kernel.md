# Native SIMD posting-list intersection / verification kernel

> Implementation-ready research for moedex (Go 1.26 trigram code-search engine).
> Scope: when, whether, and how to add a native/SIMD kernel for posting-list
> intersection and candidate verification. Generated 2026-06-22.

---

## Verdict (read this first)

**Do not write hand-SIMD yet. Profile first, and when you do optimize, almost
all of the available win is reachable in pure Go without touching assembly.**

Three reasons, in priority order:

1. **No profile exists.** moedex has never been profiled at scale. The northstar
   doc is explicit: isolate a native SIMD kernel behind a clean boundary "**only
   once profiling proves it's the bottleneck.**" We are not there. Writing SIMD
   now is speculative optimization against an unmeasured workload.

2. **The current intersection isn't even the right *algorithm* yet, let alone the
   right instruction set.** `internal/query/query.go`'s `intersect` is a textbook
   sorted-merge over `[]uint64` blob lists, and it **allocates a fresh `out`
   slice on every pairwise step** (`andQ.Eval` does N-1 of these per query). The
   first 5-50x is in data-structure and allocation choices (bitmaps / Roaring,
   reuse buffers, intersect smallest-first), all pure Go. SIMD is the *last* lever,
   not the first.

3. **Portability tax lands exactly on the dev machine.** Go 1.26's new
   `simd/archsimd` package is **amd64-only**; ARM64/SVE is explicitly deferred
   with no timeline (golang/go#73787). The dev box is darwin/arm64. A
   `simd/archsimd` kernel would not even *run accelerated* locally — it would fall
   back to scalar on the machine you develop and test on. That alone makes
   hand-SIMD premature for moedex today.

The honest one-liner: **reach for pure-Go bitmaps/Roaring and allocation hygiene
before any hand-SIMD, and only after a profile points here.**

---

## What the hot path actually is (grounding in the code)

Two candidate hot loops, matching the two query paths:

### A. Posting-list intersection — `internal/query/query.go`
- `triQ.Eval` decodes a trigram's postings (from RAM map or mmap provider) and
  reduces them to a sorted, distinct `[]uint64` of blob IDs.
- `andQ.Eval` folds those lists with `intersect(a, b)` — a 3-way-branch
  sorted-merge — once per AND term. Allocates a new slice each fold.
- `orQ.Eval` does the same with `union`.
- This is the REI/galloping/Roaring territory: many sorted integer lists, ANDed.

### B. Candidate verification — `internal/search/search.go`
- **Literal path:** builds `endAt` (a `map[uint64]map[int]bool` of end-gram
  offsets per blob — itself an allocation-heavy hot spot), intersects begin-gram
  postings against it positionally, then calls `equalAt` (a hand-rolled byte
  `memcmp`) to confirm the full literal.
- **Regex path:** for each candidate blob, `splitLines` the whole content and run
  `regexp.MatchString` per line. The regex verify is almost certainly the heavier
  of the two verify costs because Go's `regexp` (RE2-style) is not SIMD-prefiltered
  the way ripgrep's engine is.

Note both verify paths re-`string(b.Content)` and re-split lines per blob — likely
a bigger cost than the byte-compare SIMD would ever save. Measure before assuming
`equalAt` is hot.

---

## Profiling / measurement plan (do this BEFORE any optimization)

Goal: produce evidence that says either "intersection is X% of query CPU" or
"verification is Y%," with a corpus that resembles moedex's stated target.

### Corpus
- moedex's design target is **single-node, ~8GB corpus** (per project memory).
  Build (or borrow) an index at two scales: a **~1GB** smoke corpus and a **~8GB**
  target corpus. The Linux kernel tree (Cox's own benchmark, ~36k files) is a good
  reproducible mid-size stand-in; for 8GB, concatenate several large OSS repos.
- Use the **mmap-backed disk provider** (`internal/diskstore`), not the in-RAM
  builder index — the on-demand posting decode is part of the real hot path and
  changes the intersection cost profile (decode + intersect, not just intersect).

### Query mix (these stress different loops)
1. **High-selectivity literal** (rare identifier, e.g. a long unique function
   name) — few candidates, dominated by posting decode + intersect setup.
2. **Low-selectivity literal** (common token like `err` / `for` / `return`) — huge
   posting lists, this is where intersection cost peaks.
3. **Regex with weak trigram reduction** (e.g. `[A-Za-z]+Handler`) — many
   candidate blobs survive filtering, so per-line `regexp` verify dominates.
4. **Regex with strong reduction** (a literal-heavy pattern) — verify is cheap,
   intersection of several AND-terms dominates.

### Instrumentation
- `go test -bench` + `-cpuprofile` / `-memprofile` on `search.Literal`,
  `search.Regex`, and `query.andQ.Eval` directly. Add `b.ReportAllocs()`.
- `go tool pprof` flame graph; look at the **fraction of total query CPU** spent
  in (a) `intersect`/`union`, (b) posting decode, (c) `equalAt` + map building,
  (d) `regexp.*` + `splitLines`/`string()`.
- `runtime/trace` if GC pauses show up — the per-fold slice allocs in `intersect`
  and the `map[uint64]map[int]bool` in `Literal` are prime GC suspects.
- Benchmark **allocations/op** as a first-class number, not just ns/op.

### What counts as "hot enough to justify native SIMD"
A function clears the bar only if **all** hold:
- It is **>30-40% of query CPU** in the profile on the 8GB corpus, on a realistic
  query mix (not one cherry-picked pathological query), AND
- It **stays hot after** the pure-Go wins below are applied (bitmaps, buffer
  reuse, intersect-smallest-first, kill redundant `string()`/`splitLines`), AND
- A micro-benchmark of a candidate SIMD/native version shows a **multiplicative**
  (not 10-20%) speedup that survives the cgo/dispatch overhead.

If a loop is, say, 8% of CPU, Amdahl caps your whole-query win at <8% even with an
infinitely fast kernel — not worth a non-portable native dependency. Say so.

---

## Intersection-algorithm landscape (where the real speedup is)

For trigram code search the AND-of-sorted-integer-lists problem is well studied.
Speedups come in tiers; cross each tier before the next.

### Tier 0 — algorithmic / allocation hygiene (pure Go, do first)
- **Intersect smallest list first / order AND terms by selectivity.** The result of
  an AND can only shrink; starting from the shortest posting list and galloping the
  rest minimizes work. moedex currently folds in declaration order.
- **Reuse output buffers** across the N-1 folds in `andQ.Eval` instead of a fresh
  `make`/`append` each time. Kills most of the GC pressure.
- **Galloping / exponential search** when one list is far shorter than the other
  (common: a rare trigram AND a common one). Classic, branch-light, pure Go.

### Tier 1 — bitmaps / Roaring (pure Go, biggest structural win)
- Represent candidate blob sets as **bitmaps**; AND = hardware-width word `&`,
  which Go compiles to fast scalar (and the compiler/`math/bits` already use the
  best scalar ops). For dense, low-selectivity trigrams this is dramatically faster
  than element-wise merge and allocation-free.
- **Roaring bitmaps** adapt automatically: array containers for sparse sets, bitmap
  containers for dense ones, run containers for runs. CRoaring reports **4-5x over
  prior compressed-bitmap schemes** on intersection; the mature pure-Go port is
  `github.com/RoaringBitmap/roaring` (used by Bleve, etc.). **This is the single
  highest-leverage change and needs zero assembly.** Note: moedex blob IDs are
  `uint64`; Roaring is natively 32-bit (`roaring`) with a 64-bit variant
  (`roaring64`) — confirm ID range / sharding before choosing.
- **REI-style bit-vector pre-filter** (arXiv 2510.10348, the doc's cited filter
  index): a cheap per-shard bit-vector that rules out blobs before list
  intersection — ~2.1% extra storage for 14x (production) up to 379x reported
  speedups. Also pure Go (bit ops). This is a *complementary* index-structure win,
  not an instruction-set win.

### Tier 2 — hand-SIMD set intersection (native, last resort)
Only if Tier 0+1 leave intersection still dominant. State of the art:
- **SIMD galloping / merge-based vector intersection** — compare a block of one
  list against broadcasted elements of the other with packed comparisons.
- **QFilter** — merge-based, byte-check blocks via SIMD shuffle.
- **FESIA** (ICDE 2020) — bitmap-segment AND then SIMD intersect matched segments;
  O(n/√w + r).
- **Branch-reduction SIMD intersection** (VLDB) — reduces branch mispredictions;
  reported **up to 5.2x over `std::set_intersection` with SIMD, ~2.1x even without
  SIMD**. The "even without SIMD" number is the tell: much of the gain is branch
  layout, reachable in Go.

Takeaway: for code search specifically, **bitmaps/Roaring + a REI-style prefilter
capture most of the win and are pure-Go-able.** Hand-SIMD set intersection is a
single-digit multiplier on *top* of that, with a portability/maintenance cost.

---

## Verification kernel: is there a SIMD win?

### `equalAt` (byte memcmp)
Almost certainly **not worth hand-SIMD, and worse, Go already beats your loop.**
- `bytes.Equal` and `bytes.Compare` are implemented in `internal/bytealg` with
  hand-written SIMD assembly **on both amd64 (SSE2/AVX2) and arm64 (NEON)**. Just
  call `bytes.Equal(content[pos:pos+len(q)], q)` instead of the hand-rolled
  `equalAt` byte loop and you get the runtime's SIMD `memcmp` for free, on arm64
  too. (Confirm slicing doesn't realloc — it won't.)
- For the literal *search* itself, `bytes.Index` / `bytes.IndexByte` are also
  SIMD-accelerated in `internal/bytealg` (amd64 AVX2, arm64 NEON CMEQ, 1.5-8x over
  scalar). A ripgrep-style "memchr the rarest trigram byte to skip" prefilter can
  be built **entirely on top of stdlib `bytes` functions** — no custom assembly.

### Per-line regex verify
This is the likely real verify cost, and it is **structural, not SIMD**:
- Go's `regexp` (RE2 automaton) has **no SIMD literal prefilter** like Rust's
  regex/aho-corasick/Teddy. ripgrep's speed comes from staying *out* of the regex
  engine: a SIMD prefilter (memchr / Teddy multi-pattern) finds candidate lines,
  then the full engine runs only on those.
- Pure-Go wins available now, before any SIMD:
  - Stop `string(b.Content)` + `strings.Split` per blob; scan bytes and find line
    bounds with `bytes.IndexByte('\n')` (already SIMD).
  - Extract a required literal from the pattern and `bytes.Index`-prefilter each
    blob/line before invoking `regexp` — mirrors ripgrep's "literal optimization"
    using stdlib SIMD primitives.
- A genuine Teddy-class multi-literal SIMD prefilter is the *only* place a native
  kernel might later pay off on the verify side — and even then, calling out to the
  Rust `regex`/`aho-corasick` crates via cgo, or shelling to a vendored prefilter,
  is more maintainable than writing Teddy in Plan9 asm. Defer until profiled.

---

## Go SIMD options table (with portability / maintainability cost)

Dev machine is **darwin/arm64**; production target unspecified (assume amd64
Linux likely). Portability is therefore a first-class concern.

| Option | What it is | amd64 | arm64 | Maintainability | Verdict for moedex |
|---|---|---|---|---|---|
| **Pure Go + `bytes`/`math/bits`** | Stdlib already SIMD (`bytes.Equal/Index/IndexByte`); bit ops compile well | SIMD | SIMD | Trivial — no FFI, no build tags | **Use now.** Covers verify memcmp + literal skip for free, both arches. |
| **Pure Go Roaring (`RoaringBitmap/roaring`)** | Mature compressed-bitmap lib; vectorized-ish containers | fast scalar | fast scalar | Easy — one dependency, no asm | **Use for intersection** before any hand-SIMD. |
| **Go `simd/archsimd` (1.26, GOEXPERIMENT=simd)** | Official architecture-specific intrinsics | yes (128/256/512) | **NONE — amd64-only, SVE deferred, no timeline** | Experimental, no Go 1 compat promise, API unstable, needs `GOEXPERIMENT` build flag | **Avoid now.** Wouldn't accelerate on the dev machine; not stable. Revisit ~Go 1.27+. |
| **Plan9 assembly (`.s` files)** | Hand-written per-arch asm in-tree | yes | yes (must write both) | Hard — Plan9 syntax, two arch files, fragile, hard to review | Only if a profiled hot loop needs it and `archsimd` still isn't portable. |
| **`avo` (asm generator)** | Generate amd64 Plan9 asm from Go | yes | **amd64 only** | Medium for amd64; doesn't solve arm64 | Eases amd64 asm authoring but leaves arm64 gap — partial. |
| **cgo → Rust/C (e.g. CRoaring, `regex` crate)** | FFI to a mature native kernel | yes | yes | Medium — cgo build complexity, cross-compile pain, per-call FFI overhead (~tens of ns), breaks `CGO_ENABLED=0` static binary | Plausible *if* a kernel is justified; prefer reusing CRoaring/regex over writing new SIMD. |
| **Subprocess / IPC worker** | Native helper process, batched requests | yes | yes | Medium — process mgmt, serialization cost; only wins if batches are large | Last resort; serialization usually eats the SIMD win for fine-grained intersect. |

Key constraint to call out loudly: **cgo breaks Go's static-binary deployment
story**, which the northstar doc lists as a top reason to keep Go. Any native
kernel must be **optional behind a build tag with a pure-Go fallback** so
`CGO_ENABLED=0` still builds a working binary.

---

## The clean-boundary interface (so 99% stays pure Go)

Isolate the candidate-set operations behind one small interface; everything in
`query`/`search` calls only this. The native path is one build-tagged file; the
default is pure Go.

```go
// internal/setops/setops.go  (pure Go, always built)
package setops

// Intersect returns the sorted intersection of two sorted, distinct uint64
// slices, writing into dst (reused across folds) to avoid per-fold allocation.
func Intersect(dst, a, b []uint64) []uint64 { /* sorted-merge / galloping */ }

// Union mirrors Intersect.
func Union(dst, a, b []uint64) []uint64 { /* ... */ }
```

If/when Roaring or a native kernel is justified, widen the seam to a type rather
than free functions, keeping the same one-call surface:

```go
// CandidateSet abstracts the blob-id set representation. Pure-Go impls:
// sortedSlice (today) and roaringSet (tier 1). A native impl can satisfy the
// same interface without query/search knowing.
type CandidateSet interface {
    And(CandidateSet) CandidateSet
    Or(CandidateSet) CandidateSet
    Iter(func(blob uint64) bool)
    Len() int
}
```

Native dispatch with mandatory pure-Go fallback:

```go
// setops_native.go      //go:build moedex_simd && amd64 && cgo
func intersectImpl(dst, a, b []uint64) []uint64 { return nativeIntersect(dst, a, b) }

// setops_fallback.go    //go:build !moedex_simd || !amd64 || !cgo
func intersectImpl(dst, a, b []uint64) []uint64 { return goIntersect(dst, a, b) }
```

Verification seam (so the byte/regex verify can also swap a prefilter in later)
without leaking SIMD into `search`:

```go
// internal/verify
func ContainsLiteral(content, needle []byte) bool          // -> bytes.Index (SIMD today)
func EqualAt(content []byte, pos int, needle []byte) bool   // -> bytes.Equal (SIMD today)
type LinePrefilter interface{ CandidateLines(content []byte) []lineRange } // Teddy-class later
```

Properties this boundary guarantees:
- The native kernel is **one function**, optional, build-tagged, with an
  always-present pure-Go twin. `CGO_ENABLED=0` and arm64 builds are unaffected.
- `query`/`search` import `setops`/`verify` only — they never see asm or cgo.
- Correctness is testable by **differential test**: run both impls on random
  inputs and assert identical output (the existing ripgrep-parity discipline).

---

## What to do NOW vs LATER

### NOW (pure Go, no SIMD, high confidence)
1. **Profile** with the plan above on a 1GB and 8GB corpus over the 4-query mix.
   Publish the CPU/alloc breakdown. This is the gating deliverable.
2. Regardless of profile, low-risk cleanups that are strictly better:
   - Replace `equalAt` with `bytes.Equal` (free SIMD, both arches).
   - In `search.Regex`, stop `string()`+`strings.Split` per blob; scan bytes with
     `bytes.IndexByte('\n')` and add a `bytes.Index` literal prefilter per line.
   - In `andQ.Eval`, **reuse the output buffer** and **intersect smallest-first**.
   - Reconsider the `map[uint64]map[int]bool` in `search.Literal` (allocation
     heavy) — a sorted positional merge or bitmap is likely cheaper.
3. Introduce the `setops` boundary (pure-Go only) now, so the seam exists before
   it's needed.

### LATER (only if the profile says intersection/verify is still dominant)
4. Swap the sorted-slice candidate set for **pure-Go Roaring** (`roaring`/
   `roaring64`) behind the `CandidateSet` interface. Re-profile.
5. Evaluate a **REI-style bit-vector pre-filter** index structure (pure Go).
6. Only if 4-5 leave a hot loop clearing the >30-40%-of-CPU + multiplicative-
   speedup bar: consider a native kernel — and prefer **reusing CRoaring / the
   Rust `regex` crate via cgo** over hand-writing Plan9 SIMD. Reassess
   `simd/archsimd` once it has arm64 support (post Go 1.27).

### NEVER (for moedex's stated scale)
- Don't hand-write Plan9 SIMD for `equalAt` — the runtime already does it better.
- Don't adopt amd64-only `simd/archsimd` while the dev/test machine is arm64.
- Don't take a hard cgo dependency without a pure-Go fallback build.

---

## Sources

- **golang/go#73787 — `simd/archsimd` proposal.** Go 1.26 (released 2026-02-10)
  ships experimental SIMD intrinsics under `GOEXPERIMENT=simd`, **AMD64-only**;
  ARM64 SVE and a portable high-level API are explicitly deferred to later (dev.simd
  branch continues for Go 1.27). No Go 1 compat promise. Accessed 2026-06-22.
  https://github.com/golang/go/issues/73787 ; https://go.dev/doc/go1.26 ;
  https://pkg.go.dev/simd/archsimd
- **Go `internal/bytealg` SIMD.** `bytes.IndexByte`/`Index`/`Equal` use
  hand-written SIMD asm: amd64 SSE2+AVX2 (16/32 bytes/iter), arm64 NEON `CMEQ`
  (16 bytes/iter, 1.5-8x over scalar). `bytes.Index` builds on `IndexByte` for
  short needles, Rabin-Karp for long. Accessed 2026-06-22.
  https://github.com/golang/go/blob/master/src/internal/bytealg/indexbyte_amd64.s ;
  https://tip.golang.org/src/internal/bytealg/indexbyte_amd64.s
- **CRoaring / Roaring bitmaps.** Vectorized SIMD intersection/union/difference;
  container-type-adaptive AND; reported 4-5x faster intersection than Concise/WAH.
  Mature pure-Go port `github.com/RoaringBitmap/roaring`. Accessed 2026-06-22.
  https://arxiv.org/pdf/1402.6407 ;
  https://www.researchgate.net/publication/320014767
- **SIMD set intersection algorithms.** Branch-reduction SIMD intersection: up to
  5.2x over `std::set_intersection` with SIMD, ~2.1x even *without* SIMD (gain is
  largely branch layout). FESIA (ICDE 2020): bitmap-segment AND + SIMD intersect,
  O(n/√w+r). QFilter: merge-based SIMD block compare. Accessed 2026-06-22.
  https://dl.acm.org/doi/10.14778/2735508.2735518 ;
  https://users.ece.cmu.edu/~franzf/papers/icde2020_zhang.pdf ;
  https://dl.acm.org/doi/10.1145/3183713.3196924
- **ripgrep prefilter / memchr / Teddy.** Speed comes from staying out of the
  regex engine via SIMD prefilters: memchr on a rare byte to skip, Boyer-Moore for
  single substrings, Teddy (Hyperscan-derived, 16-byte packed multi-pattern,
  fast front-end + verifying back-end) for multi-literal. PCMPESTRI hardware
  substring matching usually loses to memchr+Boyer-Moore on latency/throughput.
  Accessed 2026-06-22. https://burntsushi.net/ripgrep/ ;
  https://github.com/jneem/teddy
- **REI bit-vector filter index — arXiv 2510.10348** (cited by the moedex
  northstar): ~2.1% extra storage, 14x (production) up to 379x speedups; pure bit
  ops. Benchmarked on log analysis — transfer to code search is by analogy.
  https://arxiv.org/html/2510.10348v1
- **moedex northstar** `zoekt-2026-redesign.md`, "Implementation language": Go for
  90%, native SIMD core for the hot 10%, isolated behind a clean boundary "only
  once profiling proves it's the bottleneck." 2026-06-22.
