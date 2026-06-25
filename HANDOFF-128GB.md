# Handoff — moedex redesign, moving to the 128 GB host

**Written:** 2026-06-25 (on the pre-128GB machine, mid-run).
**Author:** Claude Code session (orchestration + 3-layer verification of the redesign fan-out).
**Purpose:** Let the new 128 GB machine resume *exactly* where we left off. This file is transient — delete it once the compaction-GC lane is merged and the 128GB-gated items are scheduled.

---

## TL;DR — first thing to do on the new machine

1. Clone fresh:
   ```
   git clone git@gitlab.tcdevops.com:Zak/moedex.git && cd moedex
   ```
2. Make sure the corpus is present: **`~/TCGitlab`** (5.2 GB, ~484 git repos). The parity gates need it. It does NOT travel in the repo — copy/clone it onto the new host.
3. **The redesign is COMPLETE and merged.** compaction-GC (the final lane) was merged into `main` (`b0213a4`) and pushed on 2026-06-25 after its full-corpus parity gate PASSED. Nothing left to merge. `git pull` gets everything.
4. **Proceed to the 128GB-unblocked parked items** (section below) — that's the real remaining work. Verify `go build ./... && go build -tags onnx ./... && go vet ./... && go test ./...` is green on the new host first (sanity check the toolchain + corpus).
5. This handoff file can be deleted once you've read it — its resume-the-lane purpose is done; the durable record lives in the `moedex-redesign-slices-fanout` memory note.

---

## Repo state (all SHAs pushed to origin = gitlab.tcdevops.com:Zak/moedex)

| Ref | SHA | Meaning |
|---|---|---|
| `main` | `b9a9f59` | 8 redesign slices + served-shard dedup + delta-deduped re-export, then your ADR consolidation (added ADRs, removed stale P6/P7 reports). Pushed. |
| `redesign/compaction-gc` | `c0e3f1a` | The 9th lane — compaction-GC. **MERGED into `main` at `b0213a4` (merge commit) and pushed 2026-06-25.** Branch retained on origin for history; safe to delete. |

**Local-only state that does NOT transfer** (recreate as needed on the new host):
- git worktrees under `/Users/ZKeown/Code/moedex-wt/` — gone. Just `git checkout redesign/compaction-gc` on the new machine.
- The scratchpad and `/private/tmp/.../tasks/*.output` files (gate logs) — gone.
- `~/TCGitlab` corpus — must be present on the new host (prereq above).

---

## Compaction-GC lane (`redesign/compaction-gc`, SHA `c0e3f1a`)

**What it is:** the final deferred lane of the zoekt-2026 redesign. Both append-only content stores (CAS `blobs.pack`/`blobs.idx` MOEBLOB1; deduped served `blobs.dat` MOECONT1) accumulate **dead/unreferenced** content on repo-removal and content churn and never reclaim it. This lane adds compaction-GC: reclaim dead space by **rewriting each store from its own LIVE entries** (NOT a re-ingest/re-export from git).

**Files:**
- `internal/blobstore/compact.go` — `CompactCAS`, `CompactDedupedShardDir`, `RecoverInterruptedCASCompaction`, stats types.
- `internal/diskstore/contentstore.go` — `CompactContentStore`, `DedupedShardSHAs`, `ContentStoreSHAs`, `dedupedStoreOrder`.
- `cmd/moedex-index/main.go` — `cas-compact [-cas-dir DIR] [-shard-dir DIR]` subcommand.
- Tests: `compact_parity_test.go`, `compact_space_test.go`, `compact_swap_test.go`, `compact_orphan_test.go`, `compact_parity_corpus_test.go`.

**SACRED invariant:** never GC a still-referenced blob (= ripgrep under-approximation). Both compactors verify every live key is present in the source before writing anything; liveness is computed conservatively.

**Verification status (this machine):**
- ✅ Build (default + `-tags onnx`) + `go vet` clean.
- ✅ All unit/fixture/orphan/swap tests pass. Fixture parity gate: 352 queries / 124 rg-checked, under=0 over=0, with *real* dead blobs (removed repo + churned file).
- ✅ Codex (`gpt-5.5`) adversarial review round. Found **2**:
  - **#1 (CONFIRMED SACRED under-approx) — FIXED + verified.** `CompactDedupedShardDir` keyed liveness+carry off the manifest (`served.Shards`), but the server serves via `filepath.Glob("*.idx")` (`internal/server/corpus.go:60`, `rankcorpus.go:194`). If glob ⊋ manifest, compaction silently drops served content. Fix: drive liveness + carry-forward off the same glob; carry orphan shards (synthesize manifest entries) rather than drop. Proven by `TestCompactDedupedCarriesOrphanShard` (reverted-fails confirmed: FAILS on pre-fix `compact.go`, PASSES on fixed).
  - **#2 (OVERRULED — not reachable) — hardening applied anyway.** Codex claimed a CAS recovery roll-forward-to-corrupt-manifest crash window; traced it and it's not reachable (L197 `fsyncDirFiles` f.Sync()s the manifest *before* the swap at L205; roll-forward only fires when `casDir` is absent = post-swap = manifest already durable). Applied the cheap hardening regardless: `casDirIsComplete` now `LoadBlobManifest`-parses instead of `os.Stat`.
- ✅ **Full-corpus compaction parity gate — PASSED** (2026-06-25, 73 min, exit 0). Command:
  ```
  MOEDEX_COMPACT_PARITY_CORPUS=$HOME/TCGitlab MOEDEX_COMPACT_PARITY_MAXREPOS=0 \
    go test -count=1 -timeout 0 -run TestCompactionParityCorpus ./internal/blobstore/ -v
  ```
  (`MAXREPOS=0` = full corpus; copies it to a temp mutable dir, NEVER mutates `~/TCGitlab`. Set a smaller `MAXREPOS` e.g. 60 for a fast run.) Result: 484 repos / **186 deduped shards**; both stores `748226256B → 747931161B` reclaiming **28 dead blobs / 295 KB** (real removed-repo + churned content); **971 queries** with `CAS!=pre=0 dedup!=pre=0 CAS!=direct=0 dedup!=direct=0`; **vs-rg under(cas=0 dedup=0) over(cas=0 dedup=0)**, 1 rg-skipped. AC-D3=0 / AC-D4=0 at full scale with real dead content reclaimed → SACRED invariant holds. Optional to re-run on the new machine for fresh confirmation, but the lane is verified merge-ready.

**STATUS: compaction-GC cleared the full gauntlet and was MERGED into `main` (`b0213a4`, pushed) on 2026-06-25.** Build (default + onnx) + vet + full `go test ./...` sweep green on the merged main before push. The zoekt-2026 redesign is now functionally complete vs. the northstar, modulo the 128GB-gated items below.

**Merge: DONE.** `git merge --no-ff redesign/compaction-gc` → `b0213a4`, built/vetted/tested green, pushed. No action needed on the new machine.

---

## The redesign as a whole

The 6 original redesign slices + served-shard dedup + delta-deduped re-export are **merged into `main` and pushed**. compaction-GC is the 9th and final lane. After it merges, the zoekt-2026 redesign is functionally complete vs. the northstar (`zoekt-2026-redesign.md`), modulo the 128GB-gated scale/eval items below.

Full per-slice detail (honest negatives, codex rounds, parity SHAs) lives in the auto-memory note **`moedex-redesign-slices-fanout.md`** — read it first on the new machine for the complete picture. Other relevant memory: `moedex-host-capacity`, `moedex-p6-scale`, `moedex-pre128-sprint`, `moedex-next-lanes`.

---

## 128GB-unblocked — parked items now actionable on the new host

These were parked *because* the old host was RAM-limited (BUILD, not serve, is the constraint — see `moedex-p6-scale`). With 128 GB they're unblocked:

1. **True-8GB scale rerun.** P6 measured at 5.2 GB and *modeled* 8 GB. Re-run the scale validation against a true ~8 GB corpus now that RAM isn't the ceiling. (Build peak RSS was ~5.9 GB on the 953 MB-content / 185-shard full corpus.)
2. **P10 dense A/B.** The gated-dense arm (DenseMinQueryTerms=5, query-length gate) is implemented + complementary on the synonym-gap stratum. Run the production dense A/B at scale. ONNX path is behind `-tags onnx`.
3. **Newer dense-model selection (was "lever3", parked assuming 128GB).** Re-evaluate the embedding model now that the memory budget is large — don't pre-filter to small-RAM models.
4. **`systemd-analyze` / boot+warm timing** for the serving daemon at the larger footprint (operator runbook follow-up).

Guidance from `moedex-host-capacity`: do NOT pre-filter model/design choices to small-RAM anymore; park (don't block) only if memory is genuinely the lynchpin.

---

## Environment prereqs on the new machine

- **Go 1.26** (module `moedex`, `go 1.26` in `go.mod`).
- **ripgrep** (`rg`) on PATH — required by every parity gate (ground-truth oracle).
- **`~/TCGitlab`** corpus (5.2 GB, ~484 repos) — required by the corpus-scale gates.
- **Codex CLI** (`codex`, optional) for the cross-AI verification layer. ⚠️ GOTCHA: `codex exec` HANGS forever if stdin isn't closed — always run `codex exec ... </dev/null` and wrap in `timeout 600`. (Burned 25 min on this; don't repeat.)
- **glab** (not `gh`) for GitLab MR operations; remote is `git@gitlab.tcdevops.com:Zak/moedex.git`. MR `!1` was open at some point.
- Pure-Go zero-dependency default build is the invariant; optional deps stay behind build tags (`-tags onnx`).

---

## The verification gauntlet (reproduce for any future lane)

Every lane this session cleared the same 3-layer bar before merge:
1. **My own** build (default + onnx) + `go vet` + full package tests, and a *read* of the diff (no blind trust of the implementing agent).
2. **Codex adversarial review** (`codex exec --skip-git-repo-check --sandbox read-only ... </dev/null`) on the diff, focused on the SACRED invariant + crash-safety. Adjudicate each finding independently (1 was overruled this round); dispatch confirmed fixes back to the implementing agent, don't hand-code them.
3. **Full-corpus parity gate** over `~/TCGitlab`: AC-D3 (under-approx) = 0 and AC-D4 (over-approx) = 0 vs. ripgrep ground truth. Bug fixes get a **reverted-fails regression test** (fails on the buggy code, passes on the fix) — verified by actually reverting and re-running, not by assertion.
