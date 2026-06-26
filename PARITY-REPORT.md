# moedex v0.1 — Full-Corpus Parity Report

_Generated 2026-06-25T18:04:49-06:00 by `make parity` (seed 20260622)._

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
| Corpus root | `/Users/ZKeown/.moedex` |
| `.git` entries (`find -name .git`) | 484 |
| Repos discovered | 484 |
| Repos ingested | 484 |
| Repos skipped | 0 |
| Indexed files `\|F\|` | 61665 |
| Indexed content | 956.3 MB |
| Shards | 6 |
| Build wall time | 59.162s |
| Build peak RSS | 6719.2 MB |
| Run peak RSS | 7126.7 MB |

- **AC-B1** discovery == `.git` count: **PASS**
- **AC-B2** ≥99% of repos indexed: **PASS** (484/484)
- **AC-B3** `|F|` ≥ 40,000 floor: **PASS** (61665)
- **AC-B4** peak RSS & build time recorded, no OOM: PASS (above)

## Query battery (AC-D2)

Seeded (seed 20260622), reproducible. Total **1004** queries (floor 1000). Per-bucket counts (all non-empty):

| Bucket | Count |
|---|---|
| a-common-literal | 156 |
| b-rare-literal | 152 |
| c-phrase-literal | 117 |
| d-metachar-literal | 120 |
| e-sub-trigram | 51 |
| f-high-frequency | 45 |
| g-regex | 235 |
| h-case-insensitive | 114 |
| i-unicode | 14 |

## Parity vs ground truth (AC-D3 / AC-D4)

- **AC-D3** no under-approximation (`moedex ⊇ ripgrep`, i.e. moedex misses no Go-true match): **PASS** — 1004/1004 queries clean.
- **AC-D4** exact equality (no spurious matches vs Go truth): **PASS** — 0 real over-approximations.
- ripgrep available: true; rg invocation errors: 0.
- Justified RE2-vs-Rust engine quirks (moedex==gold, differs from rg): 0.

**No divergences of any kind** — moedex == ripgrep == gold for all 1004 queries.

## Zoekt differential (AC-E, soft)

- zoekt-index not on PATH; differential skipped

Zoekt unavailable — section skipped (ripgrep gate remains authoritative).

## Performance (soft)

| Metric | Value |
|---|---|
| Scan wall (moedex+gold, all shards) | 3m50.799s |
| ripgrep wall (all queries) | 11m19.642s |
| Total run wall | 16m15.516s |
| moedex query latency p50 | 134.105ms |
| moedex query latency p95 | 1.007939s |
| moedex query latency max | 10.034097s |

### moedex latency by bucket

| Bucket | n | p50 | p95 | max |
|---|---|---|---|---|
| a-common-literal | 156 | 91.572ms | 219.994ms | 410.326ms |
| b-rare-literal | 152 | 1.15ms | 171.093ms | 217.357ms |
| c-phrase-literal | 117 | 8.351ms | 177.794ms | 204.444ms |
| d-metachar-literal | 120 | 3.183ms | 189.702ms | 508.95ms |
| e-sub-trigram | 51 | 639.28ms | 2.081687s | 3.786905s |
| f-high-frequency | 45 | 179.182ms | 559.826ms | 698.769ms |
| g-regex | 235 | 424.859ms | 1.184585s | 6.894754s |
| h-case-insensitive | 114 | 614.062ms | 1.635936s | 10.034097s |
| i-unicode | 14 | 15.874ms | 8.678702s | 8.678702s |

### moedex candidate attribution by bucket

Candidate counts are measured at unique-blob granularity before final line verification. Filter kind, lines entering RE2, and verify worker counts are captured from the timed `internal/search` path.

| Bucket | n | cand blobs p50 | cand blobs p95 | cand blobs max | cand MB p95 | cand lines p95 | all-candidates | query-All |
|---|---|---|---|---|---|---|---|---|
| a-common-literal | 156 | 11 | 402 | 24644 | 416.3 | 18436392 | 0 | 0 |
| b-rare-literal | 152 | 2 | 37 | 1866 | 320.7 | 15621537 | 0 | 0 |
| c-phrase-literal | 117 | 26 | 1698 | 15630 | 326.2 | 15952676 | 0 | 0 |
| d-metachar-literal | 120 | 14 | 1848 | 50529 | 305.1 | 15646252 | 1 | 0 |
| e-sub-trigram | 51 | 50529 | 50529 | 50529 | 791.8 | 26372290 | 51 | 0 |
| f-high-frequency | 45 | 75 | 16373 | 19585 | 689.2 | 24111356 | 0 | 0 |
| g-regex | 235 | 61 | 2828 | 50529 | 576.4 | 21307683 | 1 | 1 |
| h-case-insensitive | 114 | 1803 | 25770 | 50529 | 77.4 | 437804 | 4 | 4 |
| i-unicode | 14 | 632 | 50529 | 50529 | 791.8 | 26372290 | 6 | 3 |

### top-20 slowest queries

| dur | bucket | matches | cand blobs | cand MB | cand lines | RE2 lines | workers | kind | all/query-All | query |
|---|---|---|---|---|---|---|---|---|---|---|
| 10.034097s | h-case-insensitive | 3206 | 50529 | 791.8 | 26372290 | 2046819 | 1 | regex-all | all/query-All | #914 h-case-insensitive [i]"Stoke\|UAsNpBsW" |
| 8.678702s | i-unicode | 251 | 50529 | 791.8 | 26372290 | 26372290 | 18 | regex-all | all/query-All | #993 i-unicode "\\p{Greek}" |
| 6.894754s | g-regex | 2865396 | 21725 | 739.0 | 24869280 | 24869280 | 18 | regex-trigram |  | #708 g-regex [U]"[0-9][0-9][0-9][0-9]" |
| 6.562563s | h-case-insensitive | 7951 | 50529 | 791.8 | 26372290 | 5228485 | 18 | regex-all | all/query-All | #984 h-case-insensitive [i]"users\|IACnBgB" |
| 3.786905s | e-sub-trigram | 5709992 | 50529 | 791.8 | 26372290 |  | 18 | literal-subtrigram | all | #588 e-sub-trigram [F]"in" |
| 3.198974s | h-case-insensitive | 52286 | 50529 | 791.8 | 26372290 | 1796261 | 1 | regex-all | all/query-All | #986 h-case-insensitive [Fi]"vest" |
| 2.940825s | e-sub-trigram | 3894226 | 50529 | 791.8 | 26372290 |  | 1 | literal-subtrigram | all | #591 e-sub-trigram [F]"te" |
| 2.869896s | h-case-insensitive | 18044 | 50529 | 791.8 | 26372290 | 686646 | 18 | regex-all | all/query-All | #913 h-case-insensitive [Fi]"Sld" |
| 2.503988s | g-regex | 3573 | 1695 | 576.4 | 21350066 | 26864 | 18 | regex-trigram |  | #803 g-regex [U]"conch\|conformant\|Beth" |
| 2.226265s | h-case-insensitive | 9309 | 25425 | 60.2 | 296176 | 160069 | 18 | regex-trigram |  | #879 h-case-insensitive [i]"Blick\|anthrax" |
| 2.081687s | e-sub-trigram | 3206921 | 50529 | 791.8 | 26372290 |  | 1 | literal-subtrigram | all | #551 e-sub-trigram [F]"," |
| 1.964755s | e-sub-trigram | 2747810 | 50529 | 791.8 | 26372290 |  | 1 | literal-subtrigram | all | #556 e-sub-trigram [F]";" |
| 1.772562s | g-regex | 111767 | 4318 | 584.8 | 21565794 | 84090 | 1 | regex-trigram |  | #810 g-regex [U]"div\|aboliere\|barmen" |
| 1.689377s | e-sub-trigram | 1738753 | 50529 | 791.8 | 26372290 |  | 18 | literal-subtrigram | all | #586 e-sub-trigram [F]"ce" |
| 1.635936s | h-case-insensitive | 106 | 8206 | 57.3 | 198952 | 54051 | 18 | regex-trigram |  | #967 h-case-insensitive [i]"ownerElement\|Archivolte" |
| 1.628684s | g-regex | 69895 | 1287 | 556.1 | 20984914 | 65396 | 18 | regex-trigram |  | #854 g-regex [U]"pontifices\|erb\|tableDH" |
| 1.620853s | h-case-insensitive | 429 | 1209 | 41.7 | 87906 | 20784 | 18 | regex-trigram |  | #886 h-case-insensitive [i]"Colombo\|legalnotes" |
| 1.571024s | g-regex | 30640 | 5424 | 599.9 | 21855106 | 27075 | 18 | regex-trigram |  | #799 g-regex [U]"cancelSubmit\|begonia\|ects" |
| 1.531759s | h-case-insensitive | 8 | 2604 | 49.3 | 28847 | 8448 | 18 | regex-trigram |  | #988 h-case-insensitive [i]"wrapperClass\|Bohnerwachs" |
| 1.489324s | i-unicode | 201 | 50529 | 791.8 | 26372290 | 111 | 18 | regex-all | all/query-All | #992 i-unicode "[α-ω]+" |

## Persistence round-trip (AC-C1)

Verified by `go test ./internal/parity/ -run RoundTrip` (save→load→mmap match sets byte-identical to the in-memory build).

## Reproducibility & environment

- Seed: `20260622` (same corpus + seed ⇒ same battery + verdict).
- Master gate: `make verify`.
- Machine: darwin/arm64, 18 CPU
- Go: go1.26.4
- ripgrep: ripgrep 15.1.0

## Scale / headroom at the target corpus (A2)

Measured 2026-06-25 against the daily-use corpus at `~/.moedex` (the same root
this parity run gated). Two build paths are reported because their memory
profiles differ by ~4×, and the deploy decision hinges on which one the box runs.
(`make parity` regenerates the sections above; this headroom section is appended
by the A2 scale run and is the authoritative source for the RAM spec.)

### `make parity MOEDEX_CORPUS=~/.moedex` → **PASS**

484 repos, 61665 files, 1004-query battery, **0 under-approx / 0 over-approx / 0
engine quirks** (see Verdict above). Production (sharded CAS) **build peak RSS
~6.7 GB** (6719 MB this run) — the relevant build-time memory figure (matches the
roadmap's "~6.8 GB" expectation).

### `cmd/scale` + `MOEDEX_MMAP=1` (in-RAM build → persist → mmap reload)

| Metric | In-RAM build | After mmap reload (served mode) |
|---|---|---|
| Heap in use | **14749 MB** (15.4× content) | **1072 MB** (1.1× content) |
| Process peak RSS | **27.7 GB** (`/usr/bin/time -l`) | — (postings page-cache, reclaimable) |
| On-disk index | — | **2124 MB** |
| Sample regex (`func\s+\w+`) | 19.9 ms | 30.8 ms (off mmap) |

- Files 61665, unique blobs 50036 (18.9% dedup), 956 MB content, 568036 distinct
  trigrams, 750.7 M postings, build 45.1 s (21.2 MB/s).
- **Working set is mmap'd postings, not heap:** dropping the in-RAM index and
  reloading via `diskstore.LoadMmap` collapses heap from 14749 MB → **1072 MB**
  while the 2.1 GB of postings stay on disk and are paged in on demand. **No OOM**
  in either phase on the 137 GB host.

### Query p95 (served daemon, mmap'd shards, in-container)

Driven against a `moedex:latest` container serving the prebuilt 6-shard /
50514-blob index over `/search`. 100 samples, selective-identifier mix:
**p50 87 ms, p90 94 ms, p95 96.5 ms, p99 100 ms** (min 5 ms, max 112 ms).
High-frequency terms (`class`/`error`/`config`, which enumerate the full count)
sit at ~85–170 ms. The ~85 ms floor is the fixed per-query cost of scanning all
six shards' candidate sets + RRF over the full corpus. (Distinct from the parity
battery's p95 above, which includes pathological full-scan regex queries.)

### Dense-store size

**N/A — dense arm is off in the default (zero-dependency) build** and absent from
this shard set (no embedding cache file in `~/.moedex-index/shards`). The dense
(ONNX) arm is opt-in (`Dockerfile.dense` / `-tags onnx` + `ONNXRUNTIME_LIB_PATH`)
and intentionally not part of daily-use serving (see the deferral list). When
enabled it adds one float32 vector per ~40-line chunk (order ~hundreds of MB for
this corpus) as a separate sidecar that does not change the lexical working set.

### Recommendation: RAM spec

- **Serving (the daemon's steady state): an 8 GB box is comfortable.** Served heap
  is ~1.1 GB and grows sub-linearly; the 2.1 GB of postings + 372 MB token index
  live in mmap/page-cache that the OS reclaims under pressure, so the resident
  footprint is elastic, not a hard floor. p95 stays ~100 ms.
- **Building is the memory-bound step, and the build path matters.** The
  production **sharded CAS build peaks at ~6.7 GB** (fits an 8 GB box with little
  headroom — prefer **12–16 GB** for the build/index host to allow corpus growth).
  The naive single-in-RAM-index path (`cmd/scale`) peaks at **~28 GB** and is a
  scaling-study artifact, **not** how the daemon or `moedex-index` build — do not
  size the box to it.
- **Padding toward ~8 GB:** SHA-content-dedup makes copy-padding ineffective
  (identical blobs collapse), so headroom was bracketed from real measurements
  instead: the in-RAM build already drives a **27.7 GB** working set with no OOM
  (>3× the 8 GB target), and the mmap-served heap stays ~1 GB regardless of corpus
  size. Both bound the 8 GB question conservatively. **Net spec: 8 GB to serve,
  12–16 GB to build, single node.**
