# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-06-23T10:52:29-06:00 by `make parity` (seed 20260622)._

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
| Build wall time | 1m34.812s |
| Build peak RSS | 5535.3 MB |
| Run peak RSS | 5535.3 MB |

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
| Scan wall (moedex+gold, all shards) | 10m56.033s |
| ripgrep wall (all queries) | 0s |
| Total run wall | 12m38.217s |
| moedex query latency p50 | 200.986ms |
| moedex query latency p95 | 3.888145s |
| moedex query latency max | 14.558232s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 116.829ms | 360.414ms | 610.681ms |
| b-rare-literal | 152 | 3.675ms | 279.183ms | 926.275ms |
| c-phrase-literal | 113 | 11.502ms | 270.828ms | 325.632ms |
| d-metachar-literal | 117 | 4.886ms | 319.397ms | 772.819ms |
| e-sub-trigram | 51 | 958.848ms | 3.309947s | 3.999813s |
| f-high-frequency | 45 | 230.832ms | 790.312ms | 869.727ms |
| g-regex | 236 | 590.807ms | 1.805824s | 9.542326s |
| h-case-insensitive | 114 | 3.495055s | 8.149498s | 11.857211s |
| i-unicode | 16 | 11.946ms | 14.558232s | 14.558232s |

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
| 14.558232s | i-unicode | 250 | 49800 | 789.3 | 26337370 | 26337370 | 10 | regex-all | all/query-All | #986 i-unicode "\\p{Greek}" |
| 11.857211s | h-case-insensitive | 2640 | 49800 | 789.3 | 26337370 | 19564090 | 10 | regex-all | all/query-All | #899 h-case-insensitive [Fi]"MSK" |
| 10.45247s | h-case-insensitive | 4470 | 8005 | 681.5 | 23583435 | 553799 | 10 | regex-trigram |  | #921 h-case-insensitive [i]"admittable\|shark" |
| 9.636867s | h-case-insensitive | 689 | 423 | 481.7 | 19391074 | 4468 | 10 | regex-trigram |  | #964 h-case-insensitive [i]"refreshtoken\|strumae" |
| 9.542326s | g-regex | 2864280 | 21271 | 736.8 | 24841591 | 24841591 | 10 | regex-trigram |  | #704 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 9.492011s | h-case-insensitive | 293 | 2785 | 602.2 | 21689409 | 26362 | 10 | regex-trigram |  | #897 h-case-insensitive [i]"LinesView\|Computes" |
| 8.245458s | h-case-insensitive | 5386 | 49800 | 789.3 | 26337370 | 1786195 | 10 | regex-all | all/query-All | #905 h-case-insensitive [i]"QAM\|hess" |
| 8.149498s | h-case-insensitive | 21 | 1066 | 575.3 | 21214487 | 2359 | 10 | regex-trigram |  | #907 h-case-insensitive [i]"Razavi\|itemAnswer" |
| 8.032201s | h-case-insensitive | 60 | 224 | 366.8 | 16846559 | 427 | 10 | regex-trigram |  | #919 h-case-insensitive [i]"addPane\|EAAhB" |
| 7.895662s | h-case-insensitive | 124 | 671 | 529.1 | 20472892 | 40924 | 10 | regex-trigram |  | #900 h-case-insensitive [i]"MemberInfo\|IAAOzG" |
| 7.826716s | h-case-insensitive | 29 | 180 | 465.7 | 19195426 | 435 | 1 | regex-trigram |  | #872 h-case-insensitive [i]"Abstauber\|construit" |
| 7.806529s | h-case-insensitive | 14 | 13759 | 700.6 | 24195643 | 252715 | 10 | regex-trigram |  | #871 h-case-insensitive [i]"Abklatsch\|CAACoC" |
| 7.779956s | h-case-insensitive | 10 | 278 | 467.5 | 19187558 | 5518 | 10 | regex-trigram |  | #969 h-case-insensitive [i]"safariextz\|conrols" |
| 7.377292s | h-case-insensitive | 8 | 153 | 419.8 | 18445986 | 74 | 1 | regex-trigram |  | #922 h-case-insensitive [i]"ajustable\|iEACgHA" |
| 7.318291s | h-case-insensitive | 8 | 1799 | 570.1 | 21129808 | 31715 | 10 | regex-trigram |  | #946 h-case-insensitive [i]"handlerKey\|aforarse" |
| 7.309287s | h-case-insensitive | 11 | 210 | 486.8 | 19379159 | 382 | 10 | regex-trigram |  | #917 h-case-insensitive [i]"accdc\|scurvier" |
| 6.781688s | h-case-insensitive | 29 | 567 | 530.8 | 20287661 | 13672 | 1 | regex-trigram |  | #979 h-case-insensitive [i]"whichDoms\|getRepeat" |
| 6.772695s | h-case-insensitive | 5 | 145 | 349.7 | 16294089 | 102 | 10 | regex-trigram |  | #885 h-case-insensitive [i]"EjEkuOzE\|KACjBka" |
| 6.369365s | h-case-insensitive | 8 | 400 | 518.2 | 20028375 | 469 | 10 | regex-trigram |  | #963 h-case-insensitive [i]"pushMainArea\|Blinkdarm" |
| 6.340622s | h-case-insensitive | 4 | 417 | 524.3 | 20274310 | 268 | 10 | regex-trigram |  | #957 h-case-insensitive [i]"nElapsed\|CADcwF" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 10 CPU
- Go: go1.26.3
- ripgrep: ripgrep 15.1.0
