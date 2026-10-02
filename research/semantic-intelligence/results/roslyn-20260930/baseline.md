# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-09-30T14:43:01-06:00 by `make parity` (seed 20260930)._

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
| Corpus root | `/private/tmp/moedex-public-oracle/corpus` |
| `.git` entries (`find -name .git`) | 1 |
| Repos discovered | 1 |
| Repos ingested | 1 |
| Repos skipped | 0 |
| Indexed files `\|F\|` | 34791 |
| Indexed content | 441.9 MB |
| Shards | 1 |
| Build wall time | 1m19.511s |
| Build peak RSS | 8459.5 MB |
| Run peak RSS | 10170.5 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (1/1)
- **AC-B3** `|F|` ≥ 40,000 floor: **FAIL** (34791)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260930), reproducible. Total **1052** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 106 |
| d-metachar-literal | 129 |
| e-sub-trigram | 50 |
| f-high-frequency | 45 |
| g-regex | 236 |
| h-case-insensitive | 114 |
| i-unicode | 64 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1052/1052 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- moedex search errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1052 queries.

## Zoekt differential (AC-E, soft)

Skipped.

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 25m57.03s |
| ripgrep wall (all queries) | 29m22.376s |
| Total run wall | 56m41.659s |
| moedex query latency p50 | 25.839ms |
| moedex query latency p95 | 623.161ms |
| moedex query latency max | 8.672173s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 10.608ms | 163.347ms | 1.089112s |
| b-rare-literal | 152 | 5.373ms | 27.76ms | 239.28ms |
| c-phrase-literal | 106 | 45.928ms | 276.017ms | 407.711ms |
| d-metachar-literal | 129 | 16.561ms | 182.352ms | 517.128ms |
| e-sub-trigram | 50 | 493.027ms | 1.860615s | 3.155505s |
| f-high-frequency | 45 | 13.779ms | 528.325ms | 752.628ms |
| g-regex | 236 | 74.431ms | 827.464ms | 4.267853s |
| h-case-insensitive | 114 | 116.537ms | 2.607932s | 5.641348s |
| i-unicode | 64 | 6.673ms | 425.046ms | 8.672173s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 7 | 1450 | 17631 | 161.1 | 3535440 | 0 | 0 |
| b-rare-literal | 152 | 2 | 187 | 4957 | 18.2 | 344035 | 0 | 0 |
| c-phrase-literal | 106 | 431 | 4432 | 20581 | 192.1 | 5045182 | 0 | 0 |
| d-metachar-literal | 129 | 98 | 3720 | 31987 | 176.6 | 4312709 | 1 | 0 |
| e-sub-trigram | 50 | 31987 | 31987 | 31987 | 440.6 | 10562633 | 50 | 0 |
| f-high-frequency | 45 | 71 | 12550 | 23264 | 368.5 | 8477949 | 0 | 0 |
| g-regex | 236 | 54 | 4375 | 31987 | 235.1 | 5291907 | 4 | 4 |
| h-case-insensitive | 114 | 2436 | 15137 | 31987 | 390.4 | 9363819 | 1 | 1 |
| i-unicode | 64 | 19 | 31987 | 31987 | 440.6 | 10562633 | 7 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 8.672173s | i-unicode | 212 | 31987 | 440.6 | 10562633 | 10562633 | 18 | regex-all | all/query-All | #992 i-unicode "\\p{Greek}" |
| 5.641348s | h-case-insensitive | 158 | 15137 | 401.0 | 9606762 | 304216 | 18 | regex-trigram |  | #965 h-case-insensitive [i]"lovat\|mystring" |
| 5.388083s | h-case-insensitive | 236776 | 10773 | 370.2 | 8850938 | 327236 | 18 | regex-trigram |  | #892 h-case-insensitive [i]"GeneralValue\|eth" |
| 4.267853s | g-regex | 7 | 17746 | 378.5 | 8858958 | 370717 | 18 | regex-trigram |  | #719 g-regex [U]"\\b(itel\|inte)\\b" |
| 4.199296s | h-case-insensitive | 388579 | 21890 | 413.3 | 9788185 | 523739 | 1 | regex-trigram |  | #879 h-case-insensitive [Fi]"Class" |
| 4.114708s | h-case-insensitive | 336 | 24433 | 424.4 | 10175844 | 682576 | 18 | regex-trigram |  | #898 h-case-insensitive [i]"Master\|Angegebener" |
| 3.76021s | g-regex | 433832 | 11942 | 340.6 | 7964575 | 7964575 | 18 | regex-trigram |  | #706 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 3.155505s | e-sub-trigram | 1853369 | 31987 | 440.6 | 10562633 |  | 1 | literal-subtrigram | all | #554 e-sub-trigram [F]";" |
| 3.03046s | h-case-insensitive | 44 | 31987 | 440.6 | 10562633 | 26621 | 18 | regex-all | all/query-All | #891 h-case-insensitive [i]"Gaga\|zakresem" |
| 2.652773s | g-regex | 314 | 1676 | 174.5 | 3752959 | 405 | 1 | regex-trigram |  | #720 g-regex [U]"\\b(kenumType\|Contracts)\\b" |
| 2.607932s | h-case-insensitive | 352632 | 13766 | 377.2 | 9090989 | 350861 | 18 | regex-trigram |  | #904 h-case-insensitive [Fi]"NULL" |
| 2.594121s | e-sub-trigram | 1707623 | 31987 | 440.6 | 10562633 |  | 18 | literal-subtrigram | all | #549 e-sub-trigram [F]"," |
| 2.445492s | h-case-insensitive | 12 | 10538 | 332.7 | 7645533 | 315626 | 18 | regex-trigram |  | #980 h-case-insensitive [Fi]"s1Source" |
| 2.424224s | h-case-insensitive | 293008 | 14289 | 390.4 | 9363819 | 292869 | 18 | regex-trigram |  | #915 h-case-insensitive [Fi]"Return" |
| 2.408644s | h-case-insensitive | 648 | 3556 | 152.3 | 3437170 | 84881 | 18 | regex-trigram |  | #951 h-case-insensitive [i]"exposed\|methodList" |
| 2.096942s | h-case-insensitive | 562072 | 20354 | 394.3 | 9458890 | 561652 | 18 | regex-trigram |  | #910 h-case-insensitive [Fi]"PUBLIC" |
| 1.948107s | g-regex | 143211 | 8884 | 360.6 | 8652023 | 143067 | 18 | regex-trigram |  | #815 g-regex [U]"encapsulados\|ldsfld\|Strin" |
| 1.860615s | e-sub-trigram | 1151928 | 31987 | 440.6 | 10562633 |  | 18 | literal-subtrigram | all | #564 e-sub-trigram [F]"Co" |
| 1.859201s | g-regex | 35 | 560 | 108.8 | 2347986 | 678 | 1 | regex-trigram |  | #721 g-regex [U]"\\b(originalC\|klere)\\b" |
| 1.734968s | h-case-insensitive | 37930 | 7015 | 322.2 | 7332334 | 43679 | 18 | regex-trigram |  | #878 h-case-insensitive [i]"Change\|konstanten" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260930` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.27.1
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
