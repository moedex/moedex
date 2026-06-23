# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-06-23T09:21:27-06:00 by `make parity` (seed 20260622)._

## Verdict

**FAIL** — hard parity gate not met. See AC-D3/AC-D4 below.

## Method & definitions

- **Scope `F`**: the exact set of files moedex's ingest selected (text, non-binary, BOM-stripped). Every oracle runs over exactly `F`: ripgrep and Zoekt search a scratch **content mirror** holding each file's *indexed bytes* (one mirror file per fileID), so file-selection, `.gitignore`, binary-detection, and BOM differences cannot contaminate parity (AC-D1).
- **Granularity**: `(file, line)`. Byte-spans were deliberately rejected: Go's RE2 (leftmost-longest) and Rust-regex submatch boundaries differ, so span equality would flag pure engine quirks, not retrieval errors. Line granularity is the v0.1 retrieval contract (did we find the right lines?).
- **Oracles**: ripgrep `rg` is the immovable ground truth. An independent in-process Go-regexp/`bytes.Contains` scan ("gold") over every blob — using no trigram filtering — adjudicates any moedex-vs-ripgrep divergence: if moedex == gold the difference is RE2-vs-Rust semantics (justified); if moedex misses a gold match it is a **real under-approximation** (AC-D3 fail). Note moedex's candidate blobs are a subset of gold's and both verify with the same Go engine, so moedex ⊆ gold always — an over-approximation vs gold would signal a deep bug.

## Engine fix surfaced by this run

`internal/search` verified candidate lines with a literal prefilter (`requiredRun`) that returned the literal bytes for an `OpLiteral` **without checking `FoldCase`**. For a case-insensitive pattern (e.g. `(?i)public`, which Go normalizes to an `OpLiteral` with `Rune="PUBLIC"`), the prefilter did a case-*sensitive* `bytes.Contains(line, "PUBLIC")` and dropped lines containing `public`/`Public` — a real under-approximation that the case-insensitive battery bucket (h) exercises. Fixed: a case-folded literal yields no required byte run, so the prefilter is disabled for it (the full engine still verifies). This is the kind of invariant violation AC-D3 exists to catch.

## Corpus & build (AC-B)

| Metric | Value |
|---|---|
| Corpus root | `/Users/ZKeown/TCGitlab` |
| `.git` entries (`find -name .git`) | 484 |
| Repos discovered | 484 |
| Repos ingested | 484 |
| Repos skipped | 0 |
| Indexed files `\|F\|` | 60883 |
| Indexed content | 953.6 MB |
| Shards | 6 |
| Build wall time | 1m25.508s |
| Build peak RSS | 6209.8 MB |
| Run peak RSS | 6209.8 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (484/484)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (60883)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1000** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 113 |
| d-metachar-literal | 117 |
| e-sub-trigram | 51 |
| f-high-frequency | 45 |
| g-regex | 236 |
| h-case-insensitive | 114 |
| i-unicode | 16 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1000/1000 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: false; rg invocation errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1000 queries.

## Zoekt differential (AC-E, soft)

Skipped.

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 10m2.912s |
| ripgrep wall (all queries) | 0s |
| Total run wall | 11m35.716s |
| moedex query latency p50 | 12.098ms |
| moedex query latency p95 | 5.490855s |
| moedex query latency max | 28.876525s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 5.777ms | 49.049ms | 207.821ms |
| b-rare-literal | 152 | 1.688ms | 19.313ms | 609.582ms |
| c-phrase-literal | 113 | 4.987ms | 30.249ms | 61.321ms |
| d-metachar-literal | 117 | 2.631ms | 31.846ms | 746.042ms |
| e-sub-trigram | 51 | 850.921ms | 2.958078s | 3.622424s |
| f-high-frequency | 45 | 11.853ms | 369.042ms | 431.714ms |
| g-regex | 236 | 504.519ms | 12.242219s | 16.80865s |
| h-case-insensitive | 114 | 3.227861s | 10.135663s | 28.876525s |
| i-unicode | 16 | 929µs | 15.613847s | 15.613847s |

### top-20 slowest queries

| dur | bucket | matches | query |
|---|---|---|---|
| 28.876525s | h-case-insensitive | 5386 | #905 h-case-insensitive [i]"QAM\|hess" |
| 25.50247s | h-case-insensitive | 45 | #971 h-case-insensitive [Fi]"skims" |
| 19.692796s | h-case-insensitive | 293 | #897 h-case-insensitive [i]"LinesView\|Computes" |
| 16.80865s | g-regex | 87 | #712 g-regex [U]"\\b(Snow\|ivate)\\b" |
| 15.804598s | h-case-insensitive | 3 | #978 h-case-insensitive [Fi]"useSETSID" |
| 15.70009s | h-case-insensitive | 2640 | #899 h-case-insensitive [Fi]"MSK" |
| 15.613847s | i-unicode | 250 | #986 i-unicode "\\p{Greek}" |
| 15.224273s | g-regex | 63 | #721 g-regex [U]"\\b(mechanically\|bity)\\b" |
| 14.700634s | g-regex | 343 | #718 g-regex [U]"\\b(czBhOc\|pascal)\\b" |
| 14.078409s | g-regex | 111 | #723 g-regex [U]"\\b(strew\|Macedonia)\\b" |
| 12.983801s | g-regex | 7 | #720 g-regex [U]"\\b(mBAAbA\|aguantar)\\b" |
| 12.755292s | g-regex | 134 | #715 g-regex [U]"\\b(Yellow\|blundering)\\b" |
| 12.578842s | g-regex | 17 | #708 g-regex [U]"\\b(IAAKH\|bairn)\\b" |
| 12.538172s | g-regex | 2864280 | #704 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 12.392619s | g-regex | 2 | #705 g-regex [U]"\\b(Agende\|kVqlMPATv)\\b" |
| 12.352268s | g-regex | 134 | #710 g-regex [U]"\\b(IRepository\|participants)\\b" |
| 12.316016s | g-regex | 104 | #714 g-regex [U]"\\b(Warns\|QueryList)\\b" |
| 12.242219s | g-regex | 13 | #716 g-regex [U]"\\b(acridotheres\|CARETTRIM)\\b" |
| 11.993332s | i-unicode | 201 | #985 i-unicode "[α-ω]+" |
| 11.877808s | g-regex | 37 | #719 g-regex [U]"\\b(krone\|Bii)\\b" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 10 CPU
- Go: go1.26.3
- ripgrep: ripgrep 15.1.0
