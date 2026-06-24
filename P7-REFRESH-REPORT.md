# P7 — Refresh correctness at scale + manifest robustness

**Goal (PRODUCTION-ROADMAP Slice P7):** measure a real incremental `refresh`
against the live corpus — blast radius (how many repos one change re-ingests),
wall time, correctness vs a full rebuild — and verify the atomic-swap + SIGHUP
loop end-to-end under concurrent query load.

**Status: PASS on correctness; the cost model is the finding.** Refresh + SIGHUP
serve query-identical, fresh content under load with **zero dropped requests**.
But for a realistic batch update (a `git pull` after some drift), the changeset
scatters across **every** shard, so refresh re-ingests **99 % of the corpus** and
fragments the shard set **6 → 480** — i.e. it costs as much as a full rebuild and
produces a worse-packed result. One real freshness **bug was found and fixed**
(commitless repos triggered a perpetual rebuild).

> Measured 2026-06-24 on a **writable scratch copy** of `~/TCGitlab` (484 repos /
> 5.2 GB) — the read-only original was never touched. The changeset is a **real
> `git pull`** across the copy (89 repos advanced HEAD), not a synthetic edit.

---

## 1. Setup

- Copied `~/TCGitlab` → scratch (preserving `.git`), giving mutable repos with
  live internal-GitLab remotes.
- Built a baseline servable shard dir (**state A**): 6 shards, 484 repos, 49,800
  blobs, 410 s, peak footprint 10.9 GB. Repo→shard distribution: **89 / 73 / 57 /
  25 / 150 / 86** (already a blast-radius warning — a change to any repo on
  shard-0004 re-ingests 150 co-resident repos).
- `git pull --ff-only` across all 484 copied repos → **state B**: 89 repos
  advanced HEAD, 5 failed benignly (4 are commitless repos with nothing to pull;
  1 transient auth/network) — failures simply leave those repos at state A.

---

## 2. Freshness bug found & fixed — commitless repos

The baseline `check`, run **immediately after a clean build**, falsely reported
**4 repos changed**: `Ansible-Roles/common.winscheduledtask`,
`docker/Images.TcConsulAgent`, `packages/apps`, `packages/nuget`. All four are
**commitless repos** (`git init`, no commit → `git rev-parse HEAD` errors, 0
tracked files, contribute 0 blobs).

Root cause: `DetectChanges` treated *any* unreadable current HEAD as
"conservatively changed," but `ingest.Head` records `""` for the same condition
at build time — so a repo that never had a HEAD was flagged changed on **every**
check, forever. At scale this means the steady-state `refresh` cron does wasted
work (carry all shards forward + rebuild the 377 MB sidecars) every cycle even
when nothing truly changed.

**Fix** (`internal/parity/manifest.go`): normalize an unreadable current HEAD to
`""` and compare to the recorded value. A repo that never had a HEAD now compares
equal (not changed); a repo that *had* a real HEAD and is now unreadable still
differs (still conservatively rebuilt). Regression test
`TestDetectChangesCommitlessRepoStable` added; all `internal/parity` tests green.
After the fix, the baseline check reports **"no changes."**

---

## 3. Blast radius (the headline)

`check` after the pull → **89 changed, 0 added, 0 removed**. Mapping the changed
repos to state-A shards:

| Metric | Value |
| --- | --- |
| Changed repos | 89 of 484 (~18 %) |
| Shards affected | **6 of 6** |
| Shards carried forward (byte copy) | **0** |
| Repos re-ingested (blast radius) | **480 of 484 (99 %)** |

Repos are interleaved into content-sized shards by ingest order, with **no
locality** — so even an 18 % changeset touches every shard, and the
"re-ingest every co-resident repo on any affected shard" strategy degenerates to
re-ingesting the whole corpus. Partial refresh only saves work when changes are
confined to a few shards (e.g. one or two repos changed between frequent
refreshes).

---

## 4. Shard explosion + dedup loss

`Rebuild` packs each re-ingested repo into **its own shard** (one-repo-per-shard,
no content-byte re-packing). With 480 repos re-ingested and 0 carried:

| | State A (build) | After refresh | Full rebuild of B |
| --- | --- | --- | --- |
| Shards | 6 | **480** | 6 |
| Blobs | 49,800 | **53,069** | 50,554 |

The refresh produces **480 shards** (vs 6) and **+2,515 blobs (+5.0 %)** vs a full
rebuild — cross-repo dedup is lost because each repo becomes its own index. The
served daemon then mmaps 480 shards (boot still fast: **2.7 s**), but this is
worse hygiene (file handles, lost dedup, fan-out width) than a clean rebuild.

---

## 5. Timing — refresh ≈ full rebuild

| Operation | Wall time | Peak footprint |
| --- | --- | --- |
| Full build (state A) | 410 s | 10.9 GB |
| **Refresh (89-repo changeset)** | **389 s** | 11.8 GB |
| Full rebuild (state B) | ~410 s | ~11 GB |

For this broad changeset the refresh (389 s) is **within noise of a full rebuild**
(410 s) — it re-ingests 99 % of repos, so there is essentially no time savings.

---

## 6. Correctness (PASS)

**Freshness probe.** `configure_cursor`, a token added by the pull (0 hits in
state A): refreshed dir → **11 matches**, state-A backup → **0**. Updated content
is served.

**Query-identical to a full rebuild.** A spot battery of 8 queries (literals +
regex, selective + broad) against the refreshed dir (480 shards) vs an
independent full rebuild of state B (6 shards) — **every query's match set is
identical** (`diff`-clean), including counts up to 51,698:

| query | refreshed | full rebuild |
| --- | ---: | ---: |
| `configure_cursor` | 11 | 11 |
| `RabbitMQ` | 1,207 | 1,207 |
| `public class` | 16,576 | 16,576 |
| `SELECT` | 10,305 | 10,305 |
| `func\s+\w+` | 151 | 151 |
| `class\s+\w+Controller` | 1,010 | 1,010 |
| `import` | 51,698 | 51,698 |

Dedup differences change storage, not per-file match sets — confirmed.

**SIGHUP hot-reload under concurrent load.** Served state A, fired 400 concurrent
`/search` requests, swapped in the refreshed shards, and `SIGHUP`'d mid-load:

- Pre-reload `configure_cursor` count = 0; post-reload = 11 (fresh content live).
- Reload landed in 6.25 s; **400 requests across the swap, 0 non-200** — no
  dropped requests during the hot-swap.

---

## 7. Findings & recommendations

1. **Refresh ≈ full rebuild for broad updates.** With no repo→shard locality, a
   realistic batch `pull` hits every shard. Partial refresh only pays off for
   small, frequent deltas (a handful of repos between cycles). For periodic bulk
   refreshes, a **full rebuild into a fresh dir + SIGHUP** costs the same and
   avoids the fragmentation below.
2. **Re-pack on rebuild (highest-value follow-up).** `Rebuild` should pack
   re-ingested repos by the same content-byte threshold `Build` uses
   (`DefaultShardBytes`), instead of one-repo-per-shard. That keeps the shard
   count ~constant (6, not 480) and preserves cross-repo dedup. Today's behavior
   inflates blobs +5 % and shards 80× after a broad refresh.
3. **Correctness is solid.** The freshness machinery — change detection,
   carry-forward, atomic swap, manifest rewrite, SIGHUP hot-reload — is correct
   and serves fresh content under load with zero drops. The issue is the cost
   model, not correctness.
4. **Fixed:** commitless repos no longer trigger perpetual rebuilds.

## Caveats

- Measured at 5.2 GB on macOS (memory under compression — see
  [`P6-SCALE-REPORT.md`](P6-SCALE-REPORT.md)); re-validate refresh timing at true
  8 GB on the 128 GB Linux host.
- The changeset is one real pull (89 repos); blast radius for a *small* delta
  (1–2 repos) would be proportionally smaller but still spans whatever shards
  those repos sit on (here, 25–150 co-resident repos per shard).
- 5 repos failed to pull (benign auth/network or commitless); they remained at
  state A and are correctly reported unchanged.
