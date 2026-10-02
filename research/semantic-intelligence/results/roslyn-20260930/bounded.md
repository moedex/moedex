# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-09-30T14:57:45-06:00 by `make parity` (seed 20260930)._

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
| Shards | 3 |
| Build wall time | 2m20.077s |
| Build peak RSS | 6516.3 MB |
| Run peak RSS | 6816.5 MB |

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
| Scan wall (moedex+gold, all shards) | 26m35.259s |
| ripgrep wall (all queries) | 28m0.459s |
| Total run wall | 56m59.85s |
| moedex query latency p50 | 40.792ms |
| moedex query latency p95 | 729.38ms |
| moedex query latency max | 9.640275s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 18.16ms | 300.357ms | 1.356108s |
| b-rare-literal | 152 | 10.697ms | 74.181ms | 289.261ms |
| c-phrase-literal | 106 | 64.116ms | 323ms | 595.868ms |
| d-metachar-literal | 129 | 26.207ms | 235.672ms | 530.18ms |
| e-sub-trigram | 50 | 599.083ms | 2.523982s | 3.51659s |
| f-high-frequency | 45 | 27.459ms | 883.912ms | 1.176513s |
| g-regex | 236 | 118.478ms | 600.251ms | 2.75937s |
| h-case-insensitive | 114 | 182.203ms | 1.250121s | 4.063605s |
| i-unicode | 64 | 11.411ms | 255.323ms | 9.640275s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 7 | 1451 | 17632 | 161.1 | 3535560 | 0 | 0 |
| b-rare-literal | 152 | 2 | 187 | 4957 | 18.2 | 344035 | 0 | 0 |
| c-phrase-literal | 106 | 431 | 4432 | 20581 | 192.1 | 5045182 | 0 | 0 |
| d-metachar-literal | 129 | 98 | 3720 | 31998 | 176.6 | 4312709 | 1 | 0 |
| e-sub-trigram | 50 | 31998 | 31998 | 31998 | 440.6 | 10562816 | 50 | 0 |
| f-high-frequency | 45 | 71 | 12554 | 23266 | 368.5 | 8478105 | 0 | 0 |
| g-regex | 236 | 54 | 4375 | 31998 | 225.4 | 5002732 | 4 | 4 |
| h-case-insensitive | 114 | 3071 | 20806 | 31998 | 29.4 | 441747 | 1 | 1 |
| i-unicode | 64 | 19 | 31998 | 31998 | 440.6 | 10562816 | 7 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 9.640275s | i-unicode | 212 | 31998 | 440.6 | 10562816 | 10562816 | 18 | regex-all | all/query-All | #992 i-unicode "\\p{Greek}" |
| 4.063605s | h-case-insensitive | 336 | 24435 | 323.9 | 7617602 | 682588 | 18 | regex-trigram |  | #898 h-case-insensitive [i]"Master\|Angegebener" |
| 3.51659s | e-sub-trigram | 1707623 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #549 e-sub-trigram [F]"," |
| 3.508075s | e-sub-trigram | 1853369 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #554 e-sub-trigram [F]";" |
| 2.75937s | g-regex | 433832 | 11946 | 340.6 | 7964741 | 7964741 | 18 | regex-trigram |  | #706 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 2.523982s | e-sub-trigram | 1151928 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #564 e-sub-trigram [F]"Co" |
| 2.347035s | h-case-insensitive | 562072 | 20428 | 40.7 | 562205 | 561893 | 18 | regex-trigram |  | #910 h-case-insensitive [Fi]"PUBLIC" |
| 2.090119s | g-regex | 4204 | 3302 | 255.1 | 5577377 | 643752 | 18 | regex-trigram |  | #657 g-regex [U]"FAWMN\|rad\|rande" |
| 1.985175s | e-sub-trigram | 747615 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #546 e-sub-trigram [F]"()" |
| 1.693011s | e-sub-trigram | 625401 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #572 e-sub-trigram [F]"Re" |
| 1.477743s | h-case-insensitive | 293008 | 14575 | 19.6 | 304757 | 292869 | 18 | regex-trigram |  | #915 h-case-insensitive [Fi]"Return" |
| 1.474715s | h-case-insensitive | 235 | 21698 | 29.4 | 464233 | 252197 | 18 | regex-trigram |  | #938 h-case-insensitive [i]"algoritmas\|interfaccia" |
| 1.41787s | h-case-insensitive | 158 | 16538 | 26.6 | 370293 | 304231 | 18 | regex-trigram |  | #965 h-case-insensitive [i]"lovat\|mystring" |
| 1.356108s | a-common-literal | 443333 | 17092 | 328.7 | 8002221 |  | 1 | literal-positional |  | #135 a-common-literal [F]"public" |
| 1.321307s | e-sub-trigram | 643601 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #582 e-sub-trigram [F]"co" |
| 1.303075s | e-sub-trigram | 342830 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #580 e-sub-trigram [F]"bo" |
| 1.273704s | i-unicode | 40018 | 31998 | 440.6 | 10562816 | 40018 | 18 | regex-all | all/query-All | #990 i-unicode "[é-ü]" |
| 1.250121s | h-case-insensitive | 44 | 31998 | 440.6 | 10562816 | 26623 | 18 | regex-all | all/query-All | #891 h-case-insensitive [i]"Gaga\|zakresem" |
| 1.214424s | e-sub-trigram | 593620 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #581 e-sub-trigram [F]"ca" |
| 1.192477s | e-sub-trigram | 57687 | 31998 | 440.6 | 10562816 |  | 18 | literal-subtrigram | all | #585 e-sub-trigram [F]"n_" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260930` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.27.1
- ripgrep: ripgrep 15.2.0 (rev e89fff89ac)
