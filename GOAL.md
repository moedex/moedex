# GOAL — moedex v0.1: full-corpus indexing + rigorous parity

> This file is the contract for an autonomous `/goal` run. Work against it until
> every **Acceptance Criterion (AC)** below passes its **Verify** command with the
> stated **Pass** condition. Do not stop early; do not declare done until the
> master gate (`make verify`) is green and `PARITY-REPORT.md` is written.

---

## 1. Mission (Definition of Done)

moedex v0.1 can **index the entire `~/TCGitlab` corpus** (all git repos under it)
and **pass rigorous exact-match retrieval parity** against ground truth, with
**Zoekt** indexed over the same corpus as a competitive differential oracle.

"Parity" here means: for a large, corpus-derived query battery, moedex returns
**every** true match (no misses — the never-under-approximate invariant) and
**no** spurious matches (exact equality with ground truth).

This is a **retrieval** milestone, not a ranking milestone. See §7 Non-Goals.

---

## 2. Ground rules (safety — these are inviolable)

1. **The corpus is READ-ONLY.** Never write, move, delete, or `git`-mutate
   anything under `~/TCGitlab`. Read files only. (The harness records corpus file
   count + a sample of mtimes before and after a run; they must be unchanged.)
2. **Never touch** `~/.claude/...` (the memory dir) or anything outside this repo
   except: reading `~/TCGitlab`, and installing Zoekt during setup.
3. **Network** is allowed ONLY for one-time tool install (`go install` of Zoekt).
   If the network is unavailable, skip the Zoekt cross-check (AC-E*) and record
   it in `PARITY-REPORT.md`; the ripgrep gate (AC-D*) remains mandatory.
4. **Bounded.** Indexing the full corpus must complete or fail gracefully within a
   wall-clock budget of **60 minutes**. If a single repo fails to ingest, log it
   and continue — do not abort the whole build. Never spin in an unbounded loop.
5. **Idempotent & reproducible.** The query battery is generated from a **fixed
   seed**; re-running the harness produces the same battery and the same verdict.
6. **Scope discipline.** Build only what the ACs require (§6 Deliverables). Do not
   refactor unrelated packages. The slice-4 byte-trigram architecture and the
   ripgrep-parity invariant are load-bearing — extend, don't rewrite.

### Anti-gaming (an autonomous run WILL be tempted; do not)
- **ripgrep is immovable ground truth.** Never weaken, narrow, or special-case the
  oracle to make moedex "agree." If moedex misses a match ripgrep found, that is a
  **real bug in moedex** — fix the root cause.
- **Never shrink, filter, or cherry-pick the query battery** to dodge a failing
  query. The battery is generated mechanically from the corpus; keep it whole.
- **Never delete, `t.Skip`, or loosen an assertion** to turn a gate green. A
  skipped test is a failed test for the purposes of this goal.
- If a hard AC genuinely cannot pass (e.g. an OOM on full-corpus build), do **not**
  fake it: record the precise blocker, the evidence, and your best partial result
  in `BLOCKERS.md`, and keep every other AC green. A documented honest blocker is
  an acceptable terminal state; a gamed green is not.

---

## 3. Definitions (so the harness is unambiguous)

- **Corpus root**: `~/TCGitlab` (override via `MOEDEX_CORPUS`).
- **Repo**: any directory containing a `.git` entry under the corpus root
  (discover by walking for `.git`; ~483 expected). `internal/ingest.Repo` ingests
  one repo today — a multi-repo discovery+ingest path is a deliverable (AC-B1).
- **Indexed file set `F`**: the exact set of files moedex's ingest selects (after
  its own binary/size/ignore filtering). This set is the **single source of truth
  for scope** — every oracle runs over **exactly `F`**, never a different set, so
  parity is never contaminated by file-selection differences. The harness must be
  able to emit `F` as an explicit path list.
- **Match**: a `(relpath, line-number, byte-span)` tuple. Two engines "agree" on a
  query iff their match sets over `F` are equal as sets of these tuples. (If byte
  spans can't be obtained identically from every oracle, fall back to
  `(relpath, line-number)` and state the granularity in the report — but prefer
  byte spans.)
- **Ground-truth oracle**: ripgrep (`rg`) run over `F` with flags matched to the
  query's semantics (literal vs regex, case sensitivity, etc.). When in doubt,
  a direct Go `regexp` scan over `F` is the tiebreak.
- **Differential oracle**: Zoekt, indexed over `F`. Used to compare competitively;
  divergences are **adjudicated by ripgrep**, never the reverse.
- **Query battery**: ≥ **1000** queries, seed-reproducible, spanning the categories
  in AC-D2. Derived from real corpus content (sampled tokens, identifiers, string
  literals) plus fixed structural regex shapes instantiated with corpus terms.

---

## 4. Acceptance Criteria

> Each AC has a **Verify** (command to run) and **Pass** (the condition).
> The master gate `make verify` (AC-G1) runs all of them and exits non-zero on any
> hard-gate failure. "Hard" = must pass for DoD; "Soft" = measured & reported, never
> blocks.

### A. Health gates (hard)
- **AC-A1 — Builds clean.** Verify: `go build ./...`. Pass: exit 0.
- **AC-A2 — Vet clean.** Verify: `go vet ./...`. Pass: exit 0, no diagnostics.
- **AC-A3 — Full unit suite green, nothing skipped.** Verify:
  `go test ./... 2>&1 | tee test.log`. Pass: exit 0 **and** `grep -c '\-\-\- SKIP' test.log` == 0 for any test that exists to guard correctness (a legitimately env-gated test like the live-corpus eval may skip only when its corpus is absent; document any skip in the report).

### B. Full-corpus indexing (hard, except where noted)
- **AC-B1 — Multi-repo discovery.** A path exists to discover every repo under the
  corpus root. Verify: the discovery count equals the on-disk `.git` count:
  `find "$MOEDEX_CORPUS" -name .git | wc -l` vs. the harness's reported repo count.
  Pass: equal (repos that fail to open are listed, not silently dropped).
- **AC-B2 — Indexes the whole corpus.** moedex ingests + indexes all discovered
  repos into a single index within the time budget. Verify: the parity harness
  build step completes exit 0. Pass: index covers ≥ 99% of discovered repos; every
  skipped repo + reason is logged to the report.
- **AC-B3 — Indexed file count is sane.** Verify: harness prints `|F|`. Pass:
  `|F|` recorded in the report and ≥ a sanity floor of 40,000 (corpus has ~138k
  files, many binary/vendored; a wildly low count signals a filtering bug).
- **AC-B4 — Peak RSS & build time (soft).** Verify: harness records peak resident
  memory and wall-clock for the full build. Pass: recorded in `PARITY-REPORT.md`.
  No hard ceiling for v0.1 (full 5.2GB is a known scaling frontier), but the run
  must **complete without OOM**; an OOM is a BLOCKERS entry, not a silent pass.

### C. Persistence round-trip (hard)
- **AC-C1 — Save/load is identical.** Build the index, `Save` it, `Load` it into a
  fresh process/struct, and run a sample of the battery through both. Verify: a
  dedicated test (`go test ./internal/parity/ -run RoundTrip`). Pass: load-side
  match sets are byte-identical to build-side for every sampled query.

### D. Parity vs ground truth — THE GATE (hard)
- **AC-D1 — Scope is pinned.** ripgrep (and Zoekt) run over **exactly `F`**
  (§3). Verify: the harness emits `F` and feeds it to `rg` (e.g. `rg --files-from`
  or explicit paths). Pass: ripgrep's scanned-file set == `F`.
- **AC-D2 — Battery coverage.** The battery (≥1000 queries, fixed seed) includes,
  each as a non-empty bucket:
  (a) common literals, (b) rare literals, (c) multi-word/phrase literals,
  (d) literals containing regex metacharacters (must be matched literally),
  (e) sub-trigram queries (1–2 chars — the trigram filter can't pre-filter these),
  (f) high-frequency terms (exercise any match-cap path),
  (g) regex: alternation, char classes, anchors (`^`/`$`), `\b` word boundaries,
  (h) case-insensitive variants of the above,
  (i) unicode / multibyte literals and classes.
  Verify: harness asserts every bucket is non-empty and prints per-bucket counts.
  Pass: all buckets non-empty; total ≥ 1000.
- **AC-D3 — No under-approximation (THE invariant).** For every battery query,
  `moedex_matches ⊇ ripgrep_matches`. Verify: `make parity` / the parity test.
  Pass: **zero** queries where moedex misses any ripgrep match. A single miss
  fails the gate — fix the engine, never the oracle.
- **AC-D4 — Exact equality (no spurious matches).** For every query,
  `moedex_matches == ripgrep_matches`. Pass: zero queries with extra moedex
  matches. (Over-approximation at the *candidate/trigram* stage is fine and
  expected; the **final verified** result set must be exact. Any genuine extra is
  a verifier bug — fix it. If a divergence is a true ripgrep/semantics quirk, it
  must be justified in the report with the exact query and bytes, and there must be
  **zero** such unjustified cases.)

### E. Zoekt differential cross-check (soft / competitive)
- **AC-E1 — Zoekt available.** Setup installs Zoekt (`go install
  github.com/sourcegraph/zoekt/cmd/zoekt-index@latest` and `.../zoekt@latest`, or
  equivalent). Verify: `zoekt-index --help` exits 0. Pass: installed, or — if
  offline — this whole section is skipped and the skip is recorded.
- **AC-E2 — Zoekt indexes the same scope.** Zoekt indexes `F` (or the repo set
  backing `F`). Recorded in the report.
- **AC-E3 — Differential report.** For the battery subset Zoekt supports, compare
  moedex vs Zoekt; adjudicate every divergence with ripgrep. Verify: harness emits
  a table. Pass (soft): `PARITY-REPORT.md` contains, per query category: count
  where moedex matched truth, count where Zoekt matched truth, and an explicit
  list of any query where **moedex missed truth but Zoekt did not** (these are
  high-priority bugs and, if present, also fail AC-D3). moedex need not equal
  Zoekt; it must never be beaten by Zoekt on truth-recall.

### F. Reproducibility & artifacts (hard)
- **AC-F1 — One-command verify.** Verify: `make verify` from a clean checkout runs
  health + build + round-trip + parity (+ Zoekt if available) and exits non-zero on
  any hard failure. Pass: exit 0 ⇒ DoD met.
- **AC-F2 — Report exists and is honest.** Verify: `PARITY-REPORT.md` is generated
  by the run. Pass: it contains `|F|`, repo counts, battery bucket counts, AC-D3 /
  AC-D4 results (pass + any justified divergences), Zoekt comparison (or skip
  note), peak RSS, build time, and query-latency p50/p95 (soft).

---

## 5. Validation protocol (how the loop knows it's done)

1. Setup: ensure `rg` present (it is); install Zoekt (AC-E1) or note offline.
2. Build the full-corpus index from `MOEDEX_CORPUS` (AC-B*).
3. Generate the seeded battery (AC-D2); fail fast if any bucket is empty.
4. Run battery through moedex and ripgrep over pinned `F`; compute AC-D3/AC-D4.
5. If Zoekt present, run the differential (AC-E*).
6. Write `PARITY-REPORT.md` (AC-F2).
7. `make verify` must exit 0. Then — and only then — DoD is met.
8. If any hard AC cannot pass for a legitimate, evidenced reason, write
   `BLOCKERS.md` (precise blocker + evidence + partial result) and leave every
   other gate green. Do not game.

---

## 6. Deliverables (build these if absent)

- **Multi-repo ingest**: discovery of all `.git` repos under a root + ingest into
  one index (extend `internal/ingest`; keep `Repo` working). (AC-B1/B2)
- **`internal/parity/`**: the harness — battery generation (seeded), the ripgrep
  oracle adapter (scope-pinned to `F`), the Zoekt differential adapter, set
  comparison at `(relpath, line, byte-span)` granularity, and `go test` entry
  points (`RoundTrip`, the parity gate). (AC-C/D/E)
- **Entrypoint**: a `Makefile` with at least `verify` and `parity` targets, and/or
  `cmd/moedex-parity`. `make verify` is the master gate. (AC-F1)
- **`PARITY-REPORT.md`** — generated artifact (AC-F2). **`BLOCKERS.md`** — only if
  a hard AC is honestly blocked.
- Update `ARCHITECTURE.md` if you add packages or on-disk formats.

---

## 7. Non-goals (explicitly OUT — do not let the run wander here)

- **Ranking quality** (BM25/dense/symbol arm tuning, nDCG/MRR thresholds). The
  hybrid ranker exists; v0.1 DoD does not gate on ranking quality.
- **Symbol-search parity** across languages, find-refs, go-to-def.
- **The embedding server / dense arm** and any network-dependent ranking.
- **MCP server behavior / agent context assembly.**
- **Multi-node / sharding / cold-tier (FM-index).** v0.1 is single-node.
- Anything requiring writes to the corpus or to claude memory.

---

## 8. Notes for the implementer

- Module is `moedex`, Go 1.26. Existing binaries: `cmd/moedex`, `cmd/moedex-mcp`,
  `cmd/scale`. On-disk formats: `MOEDEX03`/`TKI1`/`MDXE`/`SYM1` (see ARCHITECTURE.md).
- The never-under-approximate invariant lives in `internal/query` (trigram
  reduction) and `internal/search` (verification). The search verify path is
  already byte-for-byte ripgrep-parity tested at small scale — AC-D scales that
  guarantee to the full corpus and a far larger battery. A failure there is the
  most valuable thing this run can surface.
- ripgrep semantics to mirror precisely: default is line-oriented, case-sensitive,
  respects `.gitignore` (you are pinning scope to `F`, so prefer `--no-ignore`
  plus an explicit file list to remove ignore-rule ambiguity), and treats the
  pattern as a regex unless `-F`/`--fixed-strings`. Match these per query type.
