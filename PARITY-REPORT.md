# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-08-20T11:00:01-06:00 by `make parity` (seed 20260622)._

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
| Indexed files `\|F\|` | 63401 |
| Indexed content | 967.0 MB |
| Shards | 6 |
| Build wall time | 53.088s |
| Build peak RSS | 8728.7 MB |
| Run peak RSS | 9285.1 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (491/491)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (63401)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1033** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 111 |
| d-metachar-literal | 131 |
| e-sub-trigram | 50 |
| f-high-frequency | 45 |
| g-regex | 236 |
| h-case-insensitive | 114 |
| i-unicode | 38 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1033/1033 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- moedex search errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1033 queries.

## Zoekt differential (AC-E, soft)


Compared at **file** granularity vs ground truth (ripgrep). Buckets sent: a-common-literal, b-rare-literal, c-phrase-literal, d-metachar-literal, f-high-frequency, g-regex.

| Bucket | Queries | moedex⊇truth | zoekt⊇truth | zoekt skipped |
|---|---|---|---|---|
| a-common-literal | 156 | 156 | 88 | 0 |
| b-rare-literal | 152 | 152 | 95 | 0 |
| c-phrase-literal | 111 | 111 | 58 | 0 |
| d-metachar-literal | 131 | 131 | 41 | 44 |
| f-high-frequency | 45 | 45 | 16 | 0 |
| g-regex | 236 | 236 | 54 | 48 |

- Queries where **Zoekt missed truth but moedex did not** (expected — Zoekt's file/trigram caps): 387.
- Queries where **moedex missed truth but Zoekt did not** (HIGH-priority bugs; must be zero): 0.

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 4m5.709s |
| ripgrep wall (all queries) | 10m59.151s |
| Total run wall | 16m41.841s |
| moedex query latency p50 | 134.555ms |
| moedex query latency p95 | 1.145609s |
| moedex query latency max | 11.537154s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 77.939ms | 199.858ms | 399.24ms |
| b-rare-literal | 152 | 5.133ms | 184.597ms | 371.036ms |
| c-phrase-literal | 111 | 44.501ms | 193.139ms | 213ms |
| d-metachar-literal | 131 | 3.901ms | 219.875ms | 516.231ms |
| e-sub-trigram | 50 | 658.965ms | 2.591174s | 9.989485s |
| f-high-frequency | 45 | 161.392ms | 636.741ms | 697.909ms |
| g-regex | 236 | 439.84ms | 1.266042s | 7.816395s |
| h-case-insensitive | 114 | 587.465ms | 1.806543s | 8.006527s |
| i-unicode | 38 | 1.399ms | 1.594901s | 11.537154s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 8 | 375 | 26601 | 410.4 | 18256487 | 0 | 0 |
| b-rare-literal | 152 | 2 | 41 | 1262 | 351.4 | 16931662 | 0 | 0 |
| c-phrase-literal | 111 | 272 | 4918 | 17763 | 352.7 | 16136430 | 0 | 0 |
| d-metachar-literal | 131 | 39 | 2117 | 55267 | 272.9 | 15304327 | 1 | 0 |
| e-sub-trigram | 50 | 55267 | 55267 | 55267 | 853.0 | 27814831 | 50 | 0 |
| f-high-frequency | 45 | 63 | 20817 | 25832 | 765.8 | 25594219 | 0 | 0 |
| g-regex | 236 | 92 | 3991 | 55267 | 622.4 | 22170795 | 3 | 3 |
| h-case-insensitive | 114 | 1165 | 26960 | 55267 | 110.0 | 1998484 | 3 | 3 |
| i-unicode | 38 | 6 | 55267 | 55267 | 853.0 | 27814831 | 6 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 11.537154s | i-unicode | 2148 | 55267 | 853.0 | 27814831 | 27814831 | 18 | regex-all | all/query-All | #1002 i-unicode "\\p{Greek}" |
| 9.989485s | e-sub-trigram | 16975142 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #593 e-sub-trigram [F]"co" |
| 8.006527s | h-case-insensitive | 53376 | 55267 | 853.0 | 27814831 | 3784402 | 18 | regex-all | all/query-All | #886 h-case-insensitive [i]"CACvBgK\|Consul" |
| 7.816395s | g-regex | 2941946 | 24371 | 791.3 | 26096003 | 26096003 | 18 | regex-trigram |  | #695 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 5.661239s | h-case-insensitive | 2828 | 55267 | 853.0 | 27814831 | 59238 | 18 | regex-all | all/query-All | #943 h-case-insensitive [i]"bds\|aconchegar" |
| 3.274262s | h-case-insensitive | 19184 | 26658 | 323.6 | 11945834 | 857597 | 18 | regex-trigram |  | #915 h-case-insensitive [i]"Pros\|irrorFace" |
| 3.041674s | e-sub-trigram | 3823258 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #596 e-sub-trigram [F]"en" |
| 2.591174s | e-sub-trigram | 3016727 | 55267 | 853.0 | 27814831 |  | 18 | literal-subtrigram | all | #586 e-sub-trigram [F]"al" |
| 2.453851s | h-case-insensitive | 96 | 55267 | 853.0 | 27814831 | 157828 | 18 | regex-all | all/query-All | #941 h-case-insensitive [i]"badessa\|basilar" |
| 2.281005s | e-sub-trigram | 3665754 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #556 e-sub-trigram [F]"," |
| 2.269406s | e-sub-trigram | 2498463 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #594 e-sub-trigram [F]"de" |
| 2.258295s | h-case-insensitive | 5145 | 27572 | 76.2 | 640496 | 199420 | 18 | regex-trigram |  | #988 h-case-insensitive [i]"stringStart\|MARIADB" |
| 2.056788s | g-regex | 8444 | 3969 | 624.0 | 22257652 | 8059 | 18 | regex-trigram |  | #798 g-regex [U]"awe\|listElement\|idated" |
| 2.054682s | e-sub-trigram | 1867784 | 55267 | 853.0 | 27814831 |  | 18 | literal-subtrigram | all | #589 e-sub-trigram [F]"as" |
| 1.989557s | e-sub-trigram | 1528997 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #587 e-sub-trigram [F]"am" |
| 1.935738s | e-sub-trigram | 1746084 | 55267 | 853.0 | 27814831 |  | 1 | literal-subtrigram | all | #591 e-sub-trigram [F]"ce" |
| 1.898454s | g-regex | 386 | 7303 | 682.7 | 23435937 | 65516 | 18 | regex-trigram |  | #708 g-regex [U]"\\b(grano\|alle)\\b" |
| 1.890723s | e-sub-trigram | 2778653 | 55267 | 853.0 | 27814831 |  | 18 | literal-subtrigram | all | #561 e-sub-trigram [F]";" |
| 1.806543s | h-case-insensitive | 356 | 26960 | 52.0 | 369866 | 17962 | 18 | regex-trigram |  | #887 h-case-insensitive [i]"Chromium\|dataserver" |
| 1.792572s | g-regex | 22 | 3991 | 643.6 | 22635754 | 80 | 18 | regex-trigram |  | #712 g-regex [U]"\\b(indData\|constante)\\b" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.26.4
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
