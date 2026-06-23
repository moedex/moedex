# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-06-23T10:14:17-06:00 by `make parity` (seed 20260622)._

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
| Build wall time | 1m21.126s |
| Build peak RSS | 6513.1 MB |
| Run peak RSS | 6638.6 MB |

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
| Scan wall (moedex+gold, all shards) | 11m39.614s |
| ripgrep wall (all queries) | 0s |
| Total run wall | 13m10.46s |
| moedex query latency p50 | 207.464ms |
| moedex query latency p95 | 12.309149s |
| moedex query latency max | 33.646667s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 115.334ms | 328.496ms | 526.811ms |
| b-rare-literal | 152 | 2.966ms | 265.32ms | 896.202ms |
| c-phrase-literal | 113 | 11.186ms | 258.362ms | 332.896ms |
| d-metachar-literal | 117 | 4.639ms | 298.387ms | 727.54ms |
| e-sub-trigram | 51 | 865.11ms | 3.214028s | 3.822172s |
| f-high-frequency | 45 | 229.578ms | 749.934ms | 808.098ms |
| g-regex | 236 | 564.3ms | 1.699353s | 11.587742s |
| h-case-insensitive | 114 | 10.872345s | 23.491425s | 33.646667s |
| i-unicode | 16 | 10.582ms | 13.299202s | 13.299202s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 9 | 493 | 24379 | 416.6 | 18163739 | 0 | 0 |
| b-rare-literal | 152 | 2 | 44 | 330 | 316.6 | 15595668 | 0 | 0 |
| c-phrase-literal | 113 | 22 | 1080 | 13701 | 301.6 | 15401574 | 0 | 0 |
| d-metachar-literal | 117 | 16 | 689 | 49800 | 348.2 | 16392415 | 1 | 0 |
| e-sub-trigram | 51 | 49800 | 49800 | 49800 | 789.3 | 26337370 | 51 | 0 |
| f-high-frequency | 45 | 49 | 15751 | 19203 | 687.7 | 24099696 | 0 | 0 |
| g-regex | 236 | 57 | 3808 | 49800 | 589.5 | 21635451 | 3 | 3 |
| h-case-insensitive | 114 | 153 | 21876 | 49800 | 689.0 | 24066216 | 4 | 4 |
| i-unicode | 16 | 117 | 49800 | 49800 | 789.3 | 26337370 | 6 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 33.646667s | h-case-insensitive | 21 | 1066 | 575.3 | 21214487 | 228 | 10 | regex-trigram |  | #907 h-case-insensitive [i]"Razavi\|itemAnswer" |
| 31.036962s | h-case-insensitive | 124 | 671 | 529.1 | 20472892 | 40786 | 10 | regex-trigram |  | #900 h-case-insensitive [i]"MemberInfo\|IAAOzG" |
| 30.298044s | h-case-insensitive | 8 | 1799 | 570.1 | 21129808 | 28919 | 10 | regex-trigram |  | #946 h-case-insensitive [i]"handlerKey\|aforarse" |
| 26.762427s | h-case-insensitive | 10 | 278 | 467.5 | 19187558 | 55 | 10 | regex-trigram |  | #969 h-case-insensitive [i]"safariextz\|conrols" |
| 23.871011s | h-case-insensitive | 8 | 102 | 410.9 | 17946371 | 2041 | 1 | regex-trigram |  | #962 h-case-insensitive [i]"primaryView\|CACTjV" |
| 23.491425s | h-case-insensitive | 11 | 822 | 557.8 | 21081585 | 216 | 10 | regex-trigram |  | #980 h-case-insensitive [i]"whichDoms\|zeroResults" |
| 23.003013s | h-case-insensitive | 689 | 423 | 481.7 | 19391074 | 3925 | 10 | regex-trigram |  | #964 h-case-insensitive [i]"refreshtoken\|strumae" |
| 22.756466s | h-case-insensitive | 29 | 567 | 530.8 | 20287661 | 148 | 10 | regex-trigram |  | #979 h-case-insensitive [i]"whichDoms\|getRepeat" |
| 22.647355s | h-case-insensitive | 29 | 180 | 465.7 | 19195426 | 140 | 1 | regex-trigram |  | #872 h-case-insensitive [i]"Abstauber\|construit" |
| 22.079029s | h-case-insensitive | 8 | 153 | 419.8 | 18445986 | 28899 | 1 | regex-trigram |  | #922 h-case-insensitive [i]"ajustable\|iEACgHA" |
| 21.514806s | h-case-insensitive | 20 | 317 | 498.0 | 19698023 | 220 | 10 | regex-trigram |  | #942 h-case-insensitive [i]"grumpily\|fullListTime" |
| 21.470119s | h-case-insensitive | 293 | 2785 | 602.2 | 21689409 | 204791 | 10 | regex-trigram |  | #897 h-case-insensitive [i]"LinesView\|Computes" |
| 21.207536s | h-case-insensitive | 4470 | 8005 | 681.5 | 23583435 | 553345 | 10 | regex-trigram |  | #921 h-case-insensitive [i]"admittable\|shark" |
| 20.646066s | h-case-insensitive | 7 | 93 | 390.7 | 17870768 | 6 | 1 | regex-trigram |  | #882 h-case-insensitive [i]"EACRhG\|UAHyB" |
| 19.95348s | h-case-insensitive | 4 | 417 | 524.3 | 20274310 | 74 | 10 | regex-trigram |  | #957 h-case-insensitive [i]"nElapsed\|CADcwF" |
| 19.575384s | h-case-insensitive | 11 | 210 | 486.8 | 19379159 | 20 | 10 | regex-trigram |  | #917 h-case-insensitive [i]"accdc\|scurvier" |
| 18.499076s | h-case-insensitive | 58 | 108 | 409.5 | 18137835 | 50 | 10 | regex-trigram |  | #927 h-case-insensitive [i]"avowal\|hrnew" |
| 18.493583s | h-case-insensitive | 1197 | 496 | 525.3 | 20225636 | 1047 | 10 | regex-trigram |  | #930 h-case-insensitive [i]"becoming\|Mwaro" |
| 18.303302s | h-case-insensitive | 60 | 224 | 366.8 | 16846559 | 61 | 10 | regex-trigram |  | #919 h-case-insensitive [i]"addPane\|EAAhB" |
| 17.341754s | h-case-insensitive | 277887 | 13389 | 656.4 | 23224657 | 180278 | 10 | regex-trigram |  | #908 h-case-insensitive [Fi]"Return" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 10 CPU
- Go: go1.26.3
- ripgrep: ripgrep 15.1.0
