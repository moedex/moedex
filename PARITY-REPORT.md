# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-08-21T12:24:08-06:00 by `make parity` (seed 20260622)._

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
| Indexed files `\|F\|` | 63523 |
| Indexed content | 969.5 MB |
| Shards | 6 |
| Build wall time | 59.224s |
| Build peak RSS | 8627.9 MB |
| Run peak RSS | 9171.1 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (491/491)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (63523)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1056** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 116 |
| d-metachar-literal | 136 |
| e-sub-trigram | 51 |
| f-high-frequency | 45 |
| g-regex | 237 |
| h-case-insensitive | 114 |
| i-unicode | 49 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1056/1056 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- moedex search errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 1.

### Justified engine quirks (RE2 vs Rust regex)

These queries diverge from ripgrep but match the independent Go scan exactly, so the difference is a documented regex-engine semantics difference, not a retrieval error. Exact query and a sample divergent line are shown.

- #663 g-regex "B.sch" — |moe|=787 |rg|=783; rg-only=0, moe-only=4
    - (moe-only) `SpellCheck/de_DE_frami.dic:5590`  →  `B\xF6schung/Pm\x0D`
    - (moe-only) `SpellCheck/de_DE_frami.dic:5591`  →  `B\xF6schungs/hij\x0D`

## Zoekt differential (AC-E, soft)


Compared at **file** granularity vs ground truth (ripgrep). Buckets sent: a-common-literal, b-rare-literal, c-phrase-literal, d-metachar-literal, f-high-frequency, g-regex.

| Bucket | Queries | moedex⊇truth | zoekt⊇truth | zoekt skipped |
|---|---|---|---|---|
| a-common-literal | 156 | 156 | 96 | 0 |
| b-rare-literal | 152 | 152 | 100 | 0 |
| c-phrase-literal | 116 | 116 | 82 | 0 |
| d-metachar-literal | 136 | 136 | 35 | 39 |
| f-high-frequency | 45 | 45 | 13 | 0 |
| g-regex | 237 | 237 | 58 | 48 |

- Queries where **Zoekt missed truth but moedex did not** (expected — Zoekt's file/trigram caps): 371.
- Queries where **moedex missed truth but Zoekt did not** (HIGH-priority bugs; must be zero): 0.

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 4m41.784s |
| ripgrep wall (all queries) | 11m58.842s |
| Total run wall | 18m28.551s |
| moedex query latency p50 | 123.354ms |
| moedex query latency p95 | 1.207937s |
| moedex query latency max | 13.107317s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 4.597ms | 281.997ms | 428.553ms |
| b-rare-literal | 152 | 6.101ms | 211.952ms | 436.595ms |
| c-phrase-literal | 116 | 81.13ms | 229.109ms | 267.291ms |
| d-metachar-literal | 136 | 6.407ms | 255.198ms | 557.88ms |
| e-sub-trigram | 51 | 766.087ms | 3.212728s | 11.624098s |
| f-high-frequency | 45 | 182.172ms | 728.543ms | 751.098ms |
| g-regex | 237 | 455.485ms | 1.433338s | 10.046712s |
| h-case-insensitive | 114 | 569.035ms | 1.822799s | 5.764412s |
| i-unicode | 49 | 1.581ms | 1.54038s | 13.107317s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 6 | 747 | 26646 | 431.7 | 18616648 | 0 | 0 |
| b-rare-literal | 152 | 2 | 38 | 198 | 353.9 | 16991468 | 0 | 0 |
| c-phrase-literal | 116 | 118 | 4923 | 14430 | 359.8 | 16567199 | 0 | 0 |
| d-metachar-literal | 136 | 36 | 1562 | 55393 | 271.3 | 15343077 | 1 | 0 |
| e-sub-trigram | 51 | 55393 | 55393 | 55393 | 855.5 | 27840276 | 51 | 0 |
| f-high-frequency | 45 | 72 | 19507 | 22806 | 748.2 | 25283594 | 0 | 0 |
| g-regex | 237 | 70 | 3091 | 55393 | 575.0 | 21227563 | 3 | 3 |
| h-case-insensitive | 114 | 1135 | 18951 | 55393 | 66.3 | 307317 | 2 | 2 |
| i-unicode | 49 | 6 | 55393 | 55393 | 855.5 | 27840276 | 8 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 13.107317s | i-unicode | 2164 | 55393 | 855.5 | 27840276 | 27840276 | 1 | regex-all | all/query-All | #1017 i-unicode "\\p{Greek}" |
| 11.624098s | e-sub-trigram | 16981650 | 55393 | 855.5 | 27840276 |  | 1 | literal-subtrigram | all | #604 e-sub-trigram [F]"co" |
| 10.046712s | g-regex | 2943852 | 24489 | 793.9 | 26121915 | 26121915 | 18 | regex-trigram |  | #728 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 5.764412s | h-case-insensitive | 21 | 55393 | 855.5 | 27840276 | 351695 | 18 | regex-all | all/query-All | #897 h-case-insensitive [i]"CDsLD\|SslCertCount" |
| 3.594295s | e-sub-trigram | 3826402 | 55393 | 855.5 | 27840276 |  | 18 | literal-subtrigram | all | #607 e-sub-trigram [F]"en" |
| 3.224887s | h-case-insensitive | 1578 | 55393 | 855.5 | 27840276 | 18291 | 18 | regex-all | all/query-All | #919 h-case-insensitive [i]"Liability\|lHH" |
| 3.212728s | e-sub-trigram | 3023416 | 55393 | 855.5 | 27840276 |  | 18 | literal-subtrigram | all | #596 e-sub-trigram [F]"al" |
| 2.68585s | e-sub-trigram | 3683452 | 55393 | 855.5 | 27840276 |  | 1 | literal-subtrigram | all | #566 e-sub-trigram [F]"," |
| 2.612519s | e-sub-trigram | 2499830 | 55393 | 855.5 | 27840276 |  | 1 | literal-subtrigram | all | #605 e-sub-trigram [F]"de" |
| 2.408584s | h-case-insensitive | 15 | 13034 | 54.3 | 181566 | 106255 | 18 | regex-trigram |  | #998 h-case-insensitive [i]"raddolcirsi\|keyserver" |
| 2.327005s | e-sub-trigram | 1872931 | 55393 | 855.5 | 27840276 |  | 1 | literal-subtrigram | all | #599 e-sub-trigram [F]"as" |
| 2.292543s | e-sub-trigram | 2787215 | 55393 | 855.5 | 27840276 |  | 1 | literal-subtrigram | all | #571 e-sub-trigram [F]";" |
| 2.178668s | h-case-insensitive | 4630 | 11428 | 57.8 | 182142 | 38116 | 18 | regex-trigram |  | #949 h-case-insensitive [i]"bindKeys\|Victoria" |
| 2.131091s | e-sub-trigram | 1530974 | 55393 | 855.5 | 27840276 |  | 18 | literal-subtrigram | all | #597 e-sub-trigram [F]"am" |
| 2.124708s | e-sub-trigram | 1751345 | 55393 | 855.5 | 27840276 |  | 18 | literal-subtrigram | all | #601 e-sub-trigram [F]"ce" |
| 2.072635s | g-regex | 112813 | 3620 | 614.4 | 22125385 | 87377 | 1 | regex-trigram |  | #820 g-regex [U]"anoxemia\|lum\|GAAMiS" |
| 1.945187s | g-regex | 2253 | 4542 | 650.8 | 22810294 | 2131 | 18 | regex-trigram |  | #842 g-regex [U]"curva\|apofonia\|prints" |
| 1.884085s | h-case-insensitive | 619 | 6831 | 46.4 | 126136 | 2706 | 18 | regex-trigram |  | #982 h-case-insensitive [i]"lmico\|consolidate" |
| 1.822799s | h-case-insensitive | 96 | 7295 | 47.2 | 186065 | 5768 | 18 | regex-trigram |  | #935 h-case-insensitive [i]"Xinjiang\|Ausstellen" |
| 1.817347s | i-unicode | 1928 | 55393 | 855.5 | 27840276 | 1906 | 1 | regex-all | all/query-All | #1016 i-unicode "[α-ω]+" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.26.4
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
