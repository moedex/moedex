---
phase: 02-managed-corpus-integration
plan: 01
subsystem: corpus-index-rollout
tags: [git, submodules, privacy, cas, parity, launchd]

requires:
  - 01-managed-corpus-foundation
provides:
  - Managed corpus init, sync, doctor, dry-run, reindex, and deployment refresh wiring.
  - Lock-driven stable project identity across direct, parity, CAS, and served indexing.
  - Fail-closed `.ai-privacy.yml` enforcement and publication-fingerprint auditing.
  - A validated sibling corpus, CAS, MOEDEX05 shards, sidecars, and warm-reload path.
affects: [03-branch-acquisition, 04-branch-aware-cas, deployment, indexing]

actuals:
  tasks: 5
  implementation_commits: 15

tech-stack:
  added: []
  patterns:
    - Stable project ID is the acquisition-to-indexing identity.
    - Privacy policy is read before content and fingerprinted into every publication manifest.
    - Refresh advances CAS, then atomically exports shards and sidecars, then warm-reloads.

key-files:
  created:
    - internal/ingest/privacy.go
    - docs/adr/0021-ai-privacy-aware-indexing.md
    - docs/plans/phases/02-managed-corpus-integration/ROLLOUT.md
  modified:
    - cmd/moedex-corpus/main.go
    - internal/corpus/doctor.go
    - internal/ingest/ingest.go
    - internal/blobstore/build.go
    - internal/parity/corpus.go
    - scripts/refresh-corpus.sh
    - deploy/com.moedex.refresh.plist

key-decisions:
  - "Managed locks, not filesystem discovery, are authoritative for indexing."
  - "Missing policies default to level 3; effective level 1 is excluded before content reads."
  - "Root OS metadata is ignored without mutation, but managed metadata and submodule work fail closed."
  - "An observed, successful production refresh on the fixed 14:10 path gates Phase 3."

requirements-completed: []

duration: 3d elapsed
completed: "2026-08-17T08:37:52-06:00"
status: complete
---

# Phase 2: Managed Corpus Integration and Rollout Summary

**The managed snapshot is live and healthy on commit `4317b61`. The fixed production refresh
completed with exit zero, reused 852,265 dense chunks, warm-reloaded 942,867 chunks, and passed
privacy, freshness, authenticated-MCP, health, and full-corpus parity gates.**

## Accomplishments

- Added first-class `init`, managed `sync`, dry-run, layered doctor diagnostics, and one-command
  CAS refresh/export/reload orchestration to `moedex-corpus`.
- Made managed discovery lock-driven and stable-ID-aware across direct build, full parity, CAS,
  deduped served export, freshness, scale, and evaluation paths.
- Added strict privacy parsing and fail-closed enforcement before tracked content reads. Policy
  fingerprints now invalidate stale CAS, served, parity, and sidecar publications.
- Built an isolated real sibling containing 491 locked repositories, a 53,406-blob CAS, six
  MOEDEX05 served shards, token/symbol sidecars, a fingerprint-fresh 942,867-chunk dense sidecar,
  and a tested warm-reload path.
- Proved the post-refresh candidate against ripgrep over 63,268 eligible files and 1,053 queries
  with zero under-approximations, over-approximations, or oracle errors.
- Switched both launchd agents coherently to the managed corpus/CAS/shards, retained the legacy
  rollback set, and verified health, authenticated MCP, representative search, and the 14:10
  launchd calendar trigger.

## Implementation Commits

1. `212781b` — wire managed corpus lifecycle
2. `e18684e` — index managed corpus sources
3. `09d15a8` — integrate managed refresh rollout
4. `b0da921` — prove managed corpus parity
5. `434de77` — align Git-file discovery
6. `a10d9cd` — enforce AI privacy during indexing
7. `7e6b708` — capture the AI privacy boundary
8. `d9e9d6d` — reject invalid UTF-8 parity queries
9. `dbb5f04` — ignore unmanaged root noise safely
10. `15084f0` — audit published privacy references
11. `ddde968` — report restricted policy counts
12. `ccf271e` — keep managed launchd paths coherent
13. `23b22bc` — retry transient launchd bootstrap
14. `3bb3a8b` — schedule the production refresh for 14:10 local
15. `4317b61` — retain the dense reuse seed across deduped export

## Automated Evidence

- `make health` — pass: build, vet, and full `go test ./...`
- `make roundtrip` — pass
- `scripts/managed-refresh-test.sh` — pass, including forced export failure/no reload
- Managed doctor — pass for marker, lock, 491 submodules, auth, VPN/API, and Git transport
- Index doctor — six MOEDEX05 shards, zero critical findings
- Freshness check — no changed repos after the fixed production refresh
- Policy audit — 116 policies; one global level-1 repo; one level-1 path override
- Publication audit — 63,268 eligible references; zero restricted references; zero fingerprint
  mismatches or missing CAS/served identities
- Full parity — 491/491 repos, 63,268 files, 1,053 queries, zero real divergences and zero errors
- Test daemon — health, exact query, SIGHUP reload, and graceful shutdown pass
- Dense sidecar — v2/incremental-ready, 942,867 chunks, fingerprint fresh; 852,265 vectors reused
- Legacy service after token rotation — health pass and authenticated MCP `200`
- Managed production service — commit `4317b61`, 55,118 blobs, 27,854 symbol blobs, 942,867 dense
  chunks; health and authenticated MCP pass
- Production refresh agent — all three managed paths, loaded, scheduled 14:10 local; calendar
  trigger observed
- Dense wall-clock regression — cross-corpus seed reused 852,481/942,724 chunks and completed in
  11m22s; a current same-corpus repeat completed in 4.47s with nothing to embed
- Accepted production refresh — changed four projects, rewrote three shards, carried three,
  reused 852,265/942,867 dense chunks, exited zero, and completed in 23m57s (18m29s dense)
- Final parity — build 58s, Moedex+gold scan 3m59s, ripgrep 37m6s, total 42m22s; exact pass
- Zoekt — installed after final parity so future runs include its optional differential oracle

See [ROLLOUT.md](./ROLLOUT.md) and the root [PARITY-REPORT.md](../../../../PARITY-REPORT.md) for
redacted aggregate evidence and the generated full-corpus report.

## Deviations Auto-Fixed During Rollout

1. **Privacy enforcement was missing from the original indexing seam.** The first candidate init
   was stopped before CAS/index publication. A strict policy gate and ADR 0021 were added before
   the successful sibling was created.
2. **Legacy single-byte dictionary data produced invalid UTF-8 oracle patterns.** The Unicode
   sampler now extracts valid rune runs and rejects invalid generated patterns before invoking
   ripgrep.
3. **Finder metadata blocked managed sync.** The unexpected `.DS_Store` remains untouched. Doctor
   now ignores untracked root noise only; tracked metadata, gitlinks, `.moedex`, and submodule
   worktrees retain fail-closed checks.
4. **The scheduled refresh advanced four repos.** CAS added 13 blobs, rewrote four shards, carried
   two, and warm-reloaded the test daemon. Privacy, freshness, health, and full parity were rerun
   against the resulting lock.
5. **The macOS templates did not carry all three cutover paths together.** Serve could have stayed
   on legacy shards while refresh advanced the managed corpus. The installer now renders corpus,
   CAS, and shard paths coherently, with a hermetic installed-plist regression test.
6. **The initial candidate dense run had no reuse seed.** It was stopped before publication, then
   restarted from the retained v2 store. The incremental run reused 852,492 vectors and embedded
   only 90,226 new chunks.
7. **launchd briefly rejected the first production bootstrap while unloading the old KeepAlive
   job.** The exact managed plist succeeded on retry after about 83 seconds of startup downtime.
   The installer now performs a bounded retry, covered by a forced-failure shell test.
8. **The first real 14:10 refresh discarded its dense reuse seed during the deduped directory
   swap.** Sync, CAS, and shard export succeeded, but the resulting full dense rebuild was still
   running after 39 minutes and the operator terminated it without service impact. Delta export
   now leaves an unchanged served directory in place and hard-links a complete dense seed pair
   across changed-directory swaps. Repository-wide tests pass; a production-scale temporary
   benchmark completed the seeded migration in 11m22s and the steady-state repeat in 4.47s.
9. **Unattended calendar triggers lacked VPN connectivity.** Two weekend triggers and the first
   Monday kickstart failed closed at managed sync, before CAS or served publication. After VPN
   reconnection, the same registered job completed with exit zero.
10. **Warm reload did not replace the old process image.** It correctly published new data without
    downtime, but the operator wanted production to execute the installed fix as well. A deliberate
    launchd restart promoted commit `4317b61`; cold load took 10m47s under parity contention and
    then passed health, doctor, and authenticated MCP checks.

## Production Acceptance

The retained rollback index has 359 manifest heads and 48,300 served blobs. Production has 491
heads and 55,118 served blobs. Hermetic same-commit conventional/managed parity passes, and the
managed production corpus equals ripgrep and gold across all 1,053 final queries.

The operator approved the scope expansion, production switch, fixed kickstart, and 23m57s observed
refresh wall clock. The registered job exited zero after warm reload; the production daemon now
runs commit `4317b61`; privacy, freshness, health, authenticated MCP, and full parity all pass.
The legacy corpus and shards remain available for rollback. Phase 3 is unblocked.

---
*Phase: 02-managed-corpus-integration*
*Status: complete*
