# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-08-17T08:35:09-06:00 by `make parity` (seed 20260622)._

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
| Indexed files `\|F\|` | 63268 |
| Indexed content | 962.8 MB |
| Shards | 6 |
| Build wall time | 58.092s |
| Build peak RSS | 8562.8 MB |
| Run peak RSS | 9107.4 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (491/491)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (63268)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1053** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 117 |
| d-metachar-literal | 133 |
| e-sub-trigram | 49 |
| f-high-frequency | 45 |
| g-regex | 236 |
| h-case-insensitive | 114 |
| i-unicode | 51 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1053/1053 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- moedex search errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1053 queries.

## Zoekt differential (AC-E, soft)

- zoekt-index not on PATH; differential skipped

Zoekt unavailable — section skipped (ripgrep gate remains authoritative).

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 3m59.534s |
| ripgrep wall (all queries) | 37m6.434s |
| Total run wall | 42m22.231s |
| moedex query latency p50 | 114.893ms |
| moedex query latency p95 | 1.023398s |
| moedex query latency max | 11.345358s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 11.655ms | 265.527ms | 356.229ms |
| b-rare-literal | 152 | 6.198ms | 169.579ms | 354.222ms |
| c-phrase-literal | 117 | 47.86ms | 195.497ms | 227.206ms |
| d-metachar-literal | 133 | 6.999ms | 258.143ms | 474.973ms |
| e-sub-trigram | 49 | 646.788ms | 2.182962s | 9.996598s |
| f-high-frequency | 45 | 173.008ms | 572.926ms | 781.58ms |
| g-regex | 236 | 448.864ms | 1.11038s | 9.246444s |
| h-case-insensitive | 114 | 559.306ms | 1.921223s | 2.902901s |
| i-unicode | 51 | 2.382ms | 1.244434s | 11.345358s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 7 | 3253 | 26548 | 551.3 | 20527625 | 0 | 0 |
| b-rare-literal | 152 | 3 | 47 | 2326 | 334.7 | 16271773 | 0 | 0 |
| c-phrase-literal | 117 | 232 | 3006 | 14393 | 381.5 | 17112073 | 0 | 0 |
| d-metachar-literal | 133 | 48 | 1900 | 55134 | 255.6 | 14998067 | 1 | 0 |
| e-sub-trigram | 49 | 55134 | 55134 | 55134 | 848.8 | 27725459 | 49 | 0 |
| f-high-frequency | 45 | 93 | 20682 | 24497 | 742.9 | 25173384 | 0 | 0 |
| g-regex | 236 | 77 | 3163 | 55134 | 567.0 | 21013665 | 2 | 2 |
| h-case-insensitive | 114 | 1623 | 25676 | 55134 | 85.1 | 470505 | 3 | 3 |
| i-unicode | 51 | 6 | 55134 | 55134 | 848.8 | 27725459 | 8 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 11.345358s | i-unicode | 2220 | 55134 | 848.8 | 27725459 | 27725459 | 18 | regex-all | all/query-All | #1006 i-unicode "\\p{Greek}" |
| 9.996598s | e-sub-trigram | 16960149 | 55134 | 848.8 | 27725459 |  | 18 | literal-subtrigram | all | #601 e-sub-trigram [F]"co" |
| 9.246444s | g-regex | 2940514 | 24257 | 787.1 | 26007764 | 26007764 | 1 | regex-trigram |  | #709 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 2.902901s | h-case-insensitive | 2 | 55134 | 848.8 | 27725459 | 127085 | 18 | regex-all | all/query-All | #936 h-case-insensitive [Fi]"SAASsS" |
| 2.659498s | h-case-insensitive | 113 | 55134 | 848.8 | 27725459 | 636328 | 18 | regex-all | all/query-All | #940 h-case-insensitive [Fi]"WAKT" |
| 2.565188s | e-sub-trigram | 3002663 | 55134 | 848.8 | 27725459 |  | 1 | literal-subtrigram | all | #598 e-sub-trigram [F]"al" |
| 2.450707s | h-case-insensitive | 1804 | 55134 | 848.8 | 27725459 | 187957 | 18 | regex-all | all/query-All | #963 h-case-insensitive [Fi]"dts" |
| 2.333138s | h-case-insensitive | 923 | 23259 | 85.1 | 582769 | 438712 | 18 | regex-trigram |  | #919 h-case-insensitive [i]"IsUpsert\|collectible" |
| 2.182962s | e-sub-trigram | 3634702 | 55134 | 848.8 | 27725459 |  | 1 | literal-subtrigram | all | #564 e-sub-trigram [F]"," |
| 2.038633s | h-case-insensitive | 773 | 4618 | 55.3 | 118573 | 9433 | 18 | regex-trigram |  | #946 h-case-insensitive [i]"amman\|overlayColor" |
| 1.962172s | e-sub-trigram | 1855820 | 55134 | 848.8 | 27725459 |  | 18 | literal-subtrigram | all | #599 e-sub-trigram [F]"as" |
| 1.921223s | h-case-insensitive | 11 | 7106 | 57.9 | 158773 | 1438 | 18 | regex-trigram |  | #917 h-case-insensitive [i]"IAAgBjF\|ntensela" |
| 1.8782s | e-sub-trigram | 1793797 | 55134 | 848.8 | 27725459 |  | 1 | literal-subtrigram | all | #597 e-sub-trigram [F]"ac" |
| 1.774684s | e-sub-trigram | 2764605 | 55134 | 848.8 | 27725459 |  | 18 | literal-subtrigram | all | #569 e-sub-trigram [F]";" |
| 1.655629s | i-unicode | 1979 | 55134 | 848.8 | 27725459 | 1957 | 1 | regex-all | all/query-All | #1005 i-unicode "[α-ω]+" |
| 1.575752s | g-regex | 708 | 4623 | 673.1 | 23203985 | 474 | 18 | regex-trigram |  | #876 g-regex [U]"tabSize\|EAAGiC\|catar" |
| 1.570915s | h-case-insensitive | 41 | 17834 | 46.8 | 470505 | 10738 | 18 | regex-trigram |  | #918 h-case-insensitive [i]"IAElCt\|linkdomain" |
| 1.555612s | h-case-insensitive | 34 | 5405 | 45.8 | 129380 | 14261 | 18 | regex-trigram |  | #954 h-case-insensitive [i]"billingsgate\|Dhaalu" |
| 1.532883s | h-case-insensitive | 12 | 6127 | 52.3 | 108823 | 2043 | 18 | regex-trigram |  | #988 h-case-insensitive [i]"rattoppato\|EACAxI" |
| 1.456494s | g-regex | 33687 | 4882 | 628.9 | 22545480 | 29320 | 18 | regex-trigram |  | #707 g-regex [U]"YAAnC\|long\|ateneo" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.26.4
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
