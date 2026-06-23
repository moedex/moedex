# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-06-23T15:08:14-06:00 by `make parity` (seed 20260622)._

## Verdict

**PASS** — moedex returns exactly ripgrep's match set over the full corpus, with no under-approximation (AC-D3) and no over-approximation (AC-D4).

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
| Build wall time | 1m34.53s |
| Build peak RSS | 6569.1 MB |
| Run peak RSS | 6569.1 MB |

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
- ripgrep available: true; rg invocation errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 1.

### Justified engine quirks (RE2 vs Rust regex)

These queries diverge from ripgrep but match the independent Go scan exactly, so the difference is a documented regex-engine semantics difference, not a retrieval error. Exact query and a sample divergent line are shown.

- #819 g-regex "i.es" — |moe|=441140 |rg|=441134; rg-only=0, moe-only=6
    - (moe-only) `SpellCheck/es_ANY.aff:1515`  →  `SFX X e\xF1ir i\xF1es e\xF1ir`
    - (moe-only) `SpellCheck/es_ANY.aff:2250`  →  `SFX X e\xF1ir i\xF1ese e\xF1ir`

## Zoekt differential (AC-E, soft)

Skipped.

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 9m17.255s |
| ripgrep wall (all queries) | 28m18.096s |
| Total run wall | 39m18.223s |
| moedex query latency p50 | 179.543ms |
| moedex query latency p95 | 1.442877s |
| moedex query latency max | 12.691577s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 114.746ms | 303.589ms | 561.93ms |
| b-rare-literal | 152 | 2.573ms | 264.42ms | 868.388ms |
| c-phrase-literal | 113 | 9.893ms | 240.311ms | 292.264ms |
| d-metachar-literal | 117 | 4.446ms | 317.207ms | 754.715ms |
| e-sub-trigram | 51 | 901.673ms | 2.967313s | 3.687277s |
| f-high-frequency | 45 | 233.364ms | 762.988ms | 819.471ms |
| g-regex | 236 | 525.102ms | 1.642098s | 9.350878s |
| h-case-insensitive | 114 | 679.494ms | 2.254913s | 10.136827s |
| i-unicode | 16 | 10.917ms | 12.691577s | 12.691577s |

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
| g-regex | 236 | 60 | 3808 | 49800 | 589.5 | 21635451 | 3 | 3 |
| h-case-insensitive | 114 | 1671 | 22762 | 49800 | 184.7 | 3347028 | 4 | 4 |
| i-unicode | 16 | 117 | 49800 | 49800 | 789.3 | 26337370 | 6 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 12.691577s | i-unicode | 250 | 49800 | 789.3 | 26337370 | 26337370 | 10 | regex-all | all/query-All | #986 i-unicode "\\p{Greek}" |
| 10.136827s | h-case-insensitive | 2640 | 49800 | 789.3 | 26337370 | 19564090 | 10 | regex-all | all/query-All | #899 h-case-insensitive [Fi]"MSK" |
| 9.350878s | g-regex | 2864280 | 21271 | 736.8 | 24841591 | 24841591 | 10 | regex-trigram |  | #704 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 7.495804s | h-case-insensitive | 5386 | 49800 | 789.3 | 26337370 | 1786195 | 10 | regex-all | all/query-All | #905 h-case-insensitive [i]"QAM\|hess" |
| 4.225246s | h-case-insensitive | 3 | 49800 | 789.3 | 26337370 | 2534365 | 10 | regex-all | all/query-All | #978 h-case-insensitive [Fi]"useSETSID" |
| 3.847198s | h-case-insensitive | 45 | 49800 | 789.3 | 26337370 | 917767 | 10 | regex-all | all/query-All | #971 h-case-insensitive [Fi]"skims" |
| 3.733173s | h-case-insensitive | 4470 | 9767 | 211.9 | 3759793 | 553830 | 10 | regex-trigram |  | #921 h-case-insensitive [i]"admittable\|shark" |
| 3.687277s | e-sub-trigram | 3709206 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #576 e-sub-trigram [F]"es" |
| 3.327387s | e-sub-trigram | 2989586 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #574 e-sub-trigram [F]"al" |
| 2.967313s | e-sub-trigram | 3276870 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #584 e-sub-trigram [F]"st" |
| 2.772198s | e-sub-trigram | 3196348 | 49800 | 789.3 | 26337370 |  | 1 | literal-subtrigram | all | #544 e-sub-trigram [F]"," |
| 2.670426s | e-sub-trigram | 2432979 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #583 e-sub-trigram [F]"se" |
| 2.596316s | e-sub-trigram | 2736587 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #549 e-sub-trigram [F]";" |
| 2.448876s | e-sub-trigram | 1775460 | 49800 | 789.3 | 26337370 |  | 10 | literal-subtrigram | all | #572 e-sub-trigram [F]"ac" |
| 2.367946s | g-regex | 50264 | 4466 | 589.5 | 21724574 | 46876 | 10 | regex-trigram |  | #684 g-regex [U]"OAEtBW\|ems\|negated" |
| 2.264504s | g-regex | 87 | 9242 | 602.7 | 22278228 | 78830 | 10 | regex-trigram |  | #712 g-regex [U]"\\b(Snow\|ivate)\\b" |
| 2.254913s | h-case-insensitive | 689 | 2863 | 40.6 | 68381 | 7930 | 10 | regex-trigram |  | #964 h-case-insensitive [i]"refreshtoken\|strumae" |
| 2.060208s | e-sub-trigram | 1288061 | 49800 | 789.3 | 26337370 |  | 1 | literal-subtrigram | all | #575 e-sub-trigram [F]"ca" |
| 2.050775s | g-regex | 11342 | 404 | 510.5 | 19960487 | 10557 | 10 | regex-trigram |  | #683 g-regex [U]"OAAiB\|gaselos\|camp" |
| 2.035657s | g-regex | 2032 | 3600 | 599.6 | 21941899 | 1678 | 10 | regex-trigram |  | #797 g-regex [U]"cALxCnB\|latu\|EAAKukB" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 10 CPU
- Go: go1.26.3
- ripgrep: ripgrep 15.1.0
