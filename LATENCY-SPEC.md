# LATENCY-SPEC — moedex query tail latency

> Scope: cut moedex's **query latency tail** (p95/max) without weakening
> retrieval. The parity gate (`make verify`, AC-D3/AC-D4) is the immovable safety
> net: every change here must keep it GREEN — same matches, only faster. Ranking,
> indexing, and the agent API are out of scope.

## 1. Problem & evidence

Full-corpus parity run (953.6 MB, |F|=60,883, 1000-query battery) measured:

| metric | value |
|---|---|
| moedex query p50 | **10.4 ms** |
| moedex query **p95** | **15.3 s** |
| moedex query **max** | **39.0 s** |

p50 is already interactive; the **tail is the problem**. Root causes, read directly
from `internal/search/search.go` (not guessed):

1. **Sub-trigram literals** (bucket `e`: `->`, `;`, `==`, single chars). `Literal`
   with `len(q) < 3` has no trigram to intersect, so it **scans every blob, every
   line** with `bytes.Contains` (search.go ~L37). O(corpus) per query, serial.
2. **Weak-candidate regex** (bucket `g`: `^[\t ]*tok`, `\btok\b`, `t.k`, `a.*b`,
   `[0-9]{4}`). When the trigram reduction can't narrow (anchors, `.`, classes,
   alternations that degrade to `All`), `Regex` runs **RE2 `.Match` on every line
   of every candidate blob** (search.go L104-113), serial.
3. **Case-insensitive** (bucket `h`). The (correct, conservative) FoldCase fix
   disables the literal prefilter for `(?i)` literals, so those also fall to a
   **full RE2 per-line scan** over the candidate set.

Common case (literal ≥3 chars, incl. high-frequency terms) uses the positional
merge-join and is fast — **leave it alone**. The tail is specifically *serial
full-corpus scans*.

### Phase 0 measurement (DONE 2026-06-23) — confirmed & reprioritized

200-repo / 373.9 MB slice, 996-query battery, ripgrep skipped (`-no-rg`),
`-latency-csv`. (Half the full corpus, so absolute numbers run ~2× faster than the
953 MB report; the *relative* bucket ranking and root cause are scale-invariant.)

| bucket | p50 | p95 | max |
|---|---|---|---|
| **h-case-insensitive** | **5.58 s** | **11.13 s** | **13.83 s** |
| g-regex | 58 ms | 1.70 s | 5.08 s |
| e-sub-trigram | 218 ms | 1.14 s | 1.47 s |
| i-unicode | 0.56 ms | 5.89 s (n=15, one `\p{Greek}`-class query) | 5.89 s |
| f-high-frequency | 2.7 ms | 100 ms | 262 ms |
| a/b/c/d literals | <2 ms | <25 ms | <206 ms |

**The tail is overwhelmingly bucket `h`** — all top-20 slowest queries are
case-insensitive, and even the *median* `h` query is 5.6 s. Smoking gun: a `(?i)`
query matching only **5 lines still takes 11 s**, while its case-*sensitive*
rare-literal twin (bucket `b`) has p95 = **5.7 ms** — a ~2000× gap.

**Root cause (pinpointed in code):** `internal/query/cox.go:216` `literalInfo`
returns `anyInfo()` (⇒ `match: All`) for any `FoldCase` literal. So every `(?i)`
literal/alternation gets candidate set = **all blobs**, then a full RE2 per-line
scan of the whole corpus (the FoldCase prefilter fix compounds it, but candidate
selection is the dominant cost). This was a deliberate "conservatively bail rather
than enumerate the fold closure" choice — the fix is to enumerate it.

This **reprioritizes the phases below**: fold-aware candidate trigrams (a `cox.go`
change) is the ~1000× lever for the dominant bucket; intra-query parallelism is a
general ~Ncore multiplier for the residual full-scans, not the headline fix.

## 2. Hard invariant (non-negotiable)

`make verify` stays GREEN after every change: 1000/1000 AC-D3, 0 AC-D4, rg errors
0, moedex-beaten 0. The exhaustive gold oracle is the regression detector — if an
optimization drops or invents a match, the gate fails. **No prefilter or candidate
change ships without a soundness argument** (it must be a *necessary* condition for
a match; over-approximation is fine, under-approximation is a bug).

Exported signatures (`search.Literal`, `search.Regex`) are frozen — callers
(rank/eval/mcp/parity) depend on them. All work is internal.

## 3. Targets (proposed; adjust before starting)

On the full 953 MB corpus, single node:

| metric | current | target |
|---|---|---|
| p50 | 10.4 ms | ≤ 15 ms (no regression) |
| p95 | 15.3 s | **≤ 500 ms** |
| max | 39.0 s | **≤ 2 s** |

Measured by re-running `make parity` and comparing the report's latency block.

## 4. Phases (reordered per Phase-0 evidence)

### Phase 0 — Measure ✅ DONE
Instrumented `cmd/moedex-parity` (`-latency-csv`, `-no-rg`) + per-bucket and
top-20 latency tables in the report. Findings in §1 above: bucket `h` dominates;
root cause is `cox.go:216` bailing folded literals to `All`. Tooling stays in for
before/after comparison.

### Phase 1 — Fold-aware candidate trigrams (the ~1000× lever) — `internal/query/cox.go`
The dominant fix. Replace the `literalInfo` `FoldCase → anyInfo()` bail with
enumeration of the literal's **case-fold trigram closure**, so `(?i)foo` reduces to
a real trigram constraint and narrows candidates instead of selecting every blob.
- **Mechanism:** for a folded literal, build its trigrams; expand each trigram's
  runes through their **full Unicode fold orbit** via `unicode.SimpleFold` (loop
  until it cycles), and emit the AND-over-trigram-positions of the OR-over-fold-
  variants. Reuse the existing exact-set + `reCap` machinery (`exactTrigrams`,
  set-size cap → fold into `match`); on cap overflow fall back to `All` for that
  node (sound). Apply the same to folded `charClassInfo` members.
- **SOUNDNESS (this is `cox.go` — the parity invariant lives here):** the fold
  orbit must be *complete* — every rune Go's `(?i)` treats as equal must be in the
  variant set, or we under-approximate (drop matches). `unicode.SimpleFold`
  enumerates exactly Go's `(?i)` simple-fold equivalence, so it matches the engine.
  Orbits are tiny (usually 2-3), so the per-trigram blowup is bounded. **The
  exhaustive gold oracle + the case-insensitive battery bucket are the proof**;
  this is precisely the bucket that already caught the FoldCase prefilter bug.
- **Expected:** bucket `h` rare-token queries from ~11 s → ~ms (parity with their
  case-sensitive twins). Re-enabling the per-line `(?i)` prefilter (the FoldCase
  fix) becomes largely moot once candidates are narrowed; revisit only if measured.
- **Validate:** `make verify` GREEN; bucket `h` p95 within target; new `cox`
  unit tests pinning folded reductions to a non-`All` query.

**✅ DONE 2026-06-23.** `cox.go` `foldedLiteralInfo` + `asciiFoldVariants`
implemented (AND-over-clean-trigram-positions of OR-over-ASCII-fold-variants;
non-ASCII or k/s-dirty positions skipped → sound). **Parity confirmed:** 50-repo
real-ripgrep slice 0/0 under/over-approx; `TestNeverUnderApproximates` extended with
`(?i)` patterns + a long-`s` (U+017F) edge; `TestFoldedLiteralSelectivity` added.
**Latency (200-repo slice): a SOUND but PARTIAL win** — bucket `h` p50 5.58→2.29 s,
p95 11.1→7.37 s; overall p95 5.69→3.13 s. **Not the ~1000× hoped, and the run
reframed the problem:** candidate narrowing only helps when BOTH alternation
branches are constrainable, and — critically — even fully-narrowed `(?i)` queries
(e.g. `TSTAMP|loadedCount`, both branches clean, 58 matches) still cost ~7 s because
**the per-line `(?i)` prefilter is disabled**, so every line of every candidate
blob is RE2-matched. **Conclusion: Phase 2 (sound `(?i)` per-line prefilter) is
co-dominant, not optional** — it must land for the target to be reachable. Residual
also includes alternations with a genuinely-unconstrainable branch (`shrewishly|MSG`
— `MSG`'s only trigram position has the dirty `S`) → `Or(…,All)=All`; those need
Phase 2's prefilter and/or Phase 3 parallelism.

### Phase 2 — Sound `(?i)` per-line prefilter — `internal/search` — ✅ DONE 2026-06-23
The co-dominant fix Phase 1 surfaced: restore a per-line prefilter for `(?i)` (the
FoldCase fix had disabled it), so narrowed-candidate CI queries stop RE2-scanning
every line. `requiredRun`→`requiredAny` now returns a *disjunctive* litSet; a folded
literal contributes `foldedLiteralPrefilter` = the case-variant trigrams of its
FIRST clean ASCII position (e.g. `(?i)password` → `{wor,Wor,wOr,…}`), a bounded
`bytes.Contains`-able set. **Soundness** identical to Phase 1's boundary (non-ASCII
or k/s-dirty positions skipped; no clean position ⇒ nil ⇒ prefilter disabled, safe).
Non-folded behavior preserved (singleton sets; "most selective" == old "longest
run"); all `required_literals_test` cases unchanged.
- **Validated:** full suite green; `TestPrefilterSoundness_CaseInsensitive`
  (black-box through `search.Regex`, incl. a long-`s` line) + `TestRequiredLiteralsFolded`;
  real-ripgrep 50-repo slice **0/0 under/over-approx, PASS**.
- **Latency (50-repo slice, P1→P2):** bucket `h` p50 **24.5→2.68 ms (9×)**, p95
  171→103 ms; overall p95 49.8→36.6 ms. Residual = `k`/`s`-dirty literals/branches
  (`Takes|processes`, `sda`) with no clean ASCII trigram → still full-scan → Phase 3.
- **PENDING:** full-corpus P1+P2 re-measure (after the background P1 full run frees RAM).

### Phase 3 — Intra-query parallelism + conjunctive prefilters (the residual)
After P1+P2 the tail is genuinely-unavoidable full scans: `k`/`s`-dirty CI literals,
sub-trigram bucket `e` (`->`, `;`), and weak-candidate regex.
- **Intra-query parallelism:** parallelize the candidate/all-blob loops in
  `search.Literal`/`search.Regex` across workers; merge + `dedupe` at the end.
  Soundness trivial (identical matches, concurrent collection). Bound workers with a
  shared NumCPU semaphore so it doesn't oversubscribe query-parallel callers (parity).
  `go test -race ./internal/search/...` must pass.
- **Conjunctive literal prefilter** for weak regex: for `foo.*bar` / `foo\s+bar`,
  *both* runs are required → AND-of-runs prefilter (line must contain ALL). Sound,
  strictly stronger than today's pick-one.
- **Folded char-class trigrams** (extend `charClassInfo`) to narrow large unicode
  classes (`\p{Greek}`, `[α-ω]`) that currently degrade to `All`.

### Phase 4 — Deferred (only if Phases 1-3 miss target)
- SIMD posting-list intersection kernel (`[[moedex-research-roadmap]]`) — helps
  candidate selection, not the verify scan; lower priority, tail is scan-bound.
- Streaming/early-exit for `topK` callers that don't need the full match set.

## 5. Validation & exit criteria

1. `make verify` GREEN after each phase (parity unchanged) — the hard gate.
2. Latency: `make parity` report shows p95 ≤ 500 ms, max ≤ 2 s, p50 not regressed;
   per-bucket table shows no remaining bucket above target.
3. A `go test`-level microbenchmark in `internal/search` for each tail shape
   (sub-trigram literal, weak-candidate regex, `(?i)` literal) with before/after
   numbers recorded.
4. Concurrency safety: `go test -race ./internal/search/...` green (Phase 1).

## 6. Non-goals
Ranking quality, indexing throughput/RAM, the MCP/agent API, multi-node, and any
change that alters which matches are returned. This spec only makes the existing,
parity-proven results arrive faster.
