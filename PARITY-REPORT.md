# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-08-14T12:46:55-06:00 by `make parity` (seed 20260622)._

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
| Corpus root | `/Users/ZKeown/.moedex-managed` |
| `.git` entries (`find -name .git`) | 491 |
| Repos discovered | 491 |
| Repos ingested | 491 |
| Repos skipped | 0 |
| Indexed files `\|F\|` | 63225 |
| Indexed content | 962.5 MB |
| Shards | 6 |
| Build wall time | 54.907s |
| Build peak RSS | 8720.4 MB |
| Run peak RSS | 9267.4 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (491/491)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (63225)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1046** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 118 |
| d-metachar-literal | 133 |
| e-sub-trigram | 49 |
| f-high-frequency | 45 |
| g-regex | 236 |
| h-case-insensitive | 114 |
| i-unicode | 43 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1046/1046 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- moedex search errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1046 queries.

## Zoekt differential (AC-E, soft)

- zoekt-index not on PATH; differential skipped

Zoekt unavailable — section skipped (ripgrep gate remains authoritative).

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 4m5.512s |
| ripgrep wall (all queries) | 12m57.133s |
| Total run wall | 18m16.264s |
| moedex query latency p50 | 143.608ms |
| moedex query latency p95 | 1.09811s |
| moedex query latency max | 11.339572s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 7.684ms | 312.627ms | 365.317ms |
| b-rare-literal | 152 | 4.762ms | 197.203ms | 334.671ms |
| c-phrase-literal | 118 | 106.426ms | 207.811ms | 236.002ms |
| d-metachar-literal | 133 | 5.055ms | 275.467ms | 516.779ms |
| e-sub-trigram | 49 | 656.551ms | 2.257594s | 10.041145s |
| f-high-frequency | 45 | 193.715ms | 575.735ms | 867.662ms |
| g-regex | 236 | 440.965ms | 1.216789s | 9.06652s |
| h-case-insensitive | 114 | 628.551ms | 2.166399s | 4.952572s |
| i-unicode | 43 | 2.585ms | 1.280638s | 11.339572s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 6 | 4179 | 26528 | 568.9 | 20879993 | 0 | 0 |
| b-rare-literal | 152 | 3 | 57 | 481 | 349.7 | 16784724 | 0 | 0 |
| c-phrase-literal | 118 | 248 | 5008 | 17733 | 388.6 | 17043096 | 0 | 0 |
| d-metachar-literal | 133 | 27 | 1550 | 55096 | 264.1 | 15127755 | 1 | 0 |
| e-sub-trigram | 49 | 55096 | 55096 | 55096 | 848.5 | 27721022 | 49 | 0 |
| f-high-frequency | 45 | 89 | 19264 | 22548 | 742.7 | 25169288 | 0 | 0 |
| g-regex | 236 | 89 | 3856 | 55096 | 613.3 | 21837685 | 1 | 1 |
| h-case-insensitive | 114 | 2102 | 28423 | 55096 | 92.6 | 549337 | 4 | 4 |
| i-unicode | 43 | 6 | 55096 | 55096 | 848.5 | 27721022 | 9 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 11.339572s | i-unicode | 2220 | 55096 | 848.5 | 27721022 | 27721022 | 18 | regex-all | all/query-All | #1012 i-unicode "\\p{Greek}" |
| 10.041145s | e-sub-trigram | 16959368 | 55096 | 848.5 | 27721022 |  | 1 | literal-subtrigram | all | #602 e-sub-trigram [F]"co" |
| 9.06652s | g-regex | 2940168 | 24231 | 787.0 | 26004373 | 26004373 | 18 | regex-trigram |  | #712 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 4.952572s | h-case-insensitive | 121 | 55096 | 848.5 | 27721022 | 201804 | 18 | regex-all | all/query-All | #954 h-case-insensitive [i]"billies\|DTKit" |
| 4.158407s | h-case-insensitive | 399 | 55096 | 848.5 | 27721022 | 127901 | 18 | regex-all | all/query-All | #999 h-case-insensitive [i]"warmed\|aASM" |
| 2.745448s | h-case-insensitive | 13 | 55096 | 848.5 | 27721022 | 127084 | 18 | regex-all | all/query-All | #936 h-case-insensitive [Fi]"SAASoc" |
| 2.601696s | h-case-insensitive | 31 | 55096 | 848.5 | 27721022 | 156689 | 18 | regex-all | all/query-All | #931 h-case-insensitive [Fi]"PKIX" |
| 2.599684s | e-sub-trigram | 3001918 | 55096 | 848.5 | 27721022 |  | 1 | literal-subtrigram | all | #599 e-sub-trigram [F]"al" |
| 2.466008s | h-case-insensitive | 42 | 16884 | 80.3 | 338080 | 303906 | 18 | regex-trigram |  | #946 h-case-insensitive [i]"ammalizzito\|outsb" |
| 2.257594s | e-sub-trigram | 3633624 | 55096 | 848.5 | 27721022 |  | 1 | literal-subtrigram | all | #565 e-sub-trigram [F]"," |
| 2.166399s | h-case-insensitive | 36 | 20056 | 83.3 | 433432 | 352008 | 18 | regex-trigram |  | #990 h-case-insensitive [i]"rokrat\|travisato" |
| 2.06933s | e-sub-trigram | 1855142 | 55096 | 848.5 | 27721022 |  | 1 | literal-subtrigram | all | #600 e-sub-trigram [F]"as" |
| 1.96117s | e-sub-trigram | 1792968 | 55096 | 848.5 | 27721022 |  | 18 | literal-subtrigram | all | #598 e-sub-trigram [F]"ac" |
| 1.900157s | e-sub-trigram | 2763304 | 55096 | 848.5 | 27721022 |  | 1 | literal-subtrigram | all | #570 e-sub-trigram [F]";" |
| 1.709164s | g-regex | 2069 | 900 | 510.1 | 19818015 | 2611 | 18 | regex-trigram |  | #673 g-regex [U]"EAqBAK\|calculate\|commonY" |
| 1.697883s | h-case-insensitive | 423 | 16121 | 55.8 | 238241 | 4679 | 18 | regex-trigram |  | #993 h-case-insensitive [i]"startY\|linkcode" |
| 1.618642s | i-unicode | 1979 | 55096 | 848.5 | 27721022 | 1957 | 18 | regex-all | all/query-All | #1011 i-unicode "[α-ω]+" |
| 1.563508s | h-case-insensitive | 2028 | 2346 | 35.3 | 23483 | 1277 | 18 | regex-trigram |  | #994 h-case-insensitive [i]"stepBackward\|RuleLock" |
| 1.54237s | h-case-insensitive | 9970 | 5942 | 40.8 | 121765 | 12720 | 1 | regex-trigram |  | #955 h-case-insensitive [i]"brazen\|freedom" |
| 1.526887s | g-regex | 9947 | 515 | 483.8 | 19391524 | 8744 | 1 | regex-trigram |  | #833 g-regex [U]"erfolgreich\|nid\|KANJ" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.26.4
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
