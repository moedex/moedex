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

duration: 3h23m
completed: null
status: awaiting-production-refresh
---

# Phase 2: Managed Corpus Integration and Rollout Summary

**The managed snapshot is live and healthy. The 14:10 calendar trigger was proven, but its first
production run exposed and was terminated for a dense reuse-seed bug; the tested fix is installed
and awaits explicit approval for one production kickstart.**

## Accomplishments

- Added first-class `init`, managed `sync`, dry-run, layered doctor diagnostics, and one-command
  CAS refresh/export/reload orchestration to `moedex-corpus`.
- Made managed discovery lock-driven and stable-ID-aware across direct build, full parity, CAS,
  deduped served export, freshness, scale, and evaluation paths.
- Added strict privacy parsing and fail-closed enforcement before tracked content reads. Policy
  fingerprints now invalidate stale CAS, served, parity, and sidecar publications.
- Built an isolated real sibling containing 491 locked repositories, a 53,205-blob CAS, six
  MOEDEX05 served shards, token/symbol sidecars, a fingerprint-fresh 942,718-chunk dense sidecar,
  and a tested warm-reload path.
- Proved the post-refresh candidate against ripgrep over 63,225 eligible files and 1,046 queries
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
- Freshness check — no changed repos after scheduled-equivalent refresh
- Policy audit — 116 policies; one global level-1 repo; one level-1 path override
- Publication audit — 63,225 eligible references; zero restricted references; zero fingerprint
  mismatches or missing CAS/served identities
- Full parity — 491/491 repos, 63,225 files, 1,046 queries, zero real divergences and zero errors
- Test daemon — health, exact query, SIGHUP reload, and graceful shutdown pass
- Dense sidecar — v2/incremental-ready, 942,718 chunks, fingerprint fresh; 852,492 vectors reused
- Legacy service after token rotation — health pass and authenticated MCP `200`
- Managed production service — 55,080 blobs, 27,833 symbol blobs, 942,718 dense chunks; health,
  authenticated MCP, and redacted representative search pass
- Production refresh agent — all three managed paths, loaded, scheduled 14:10 local; calendar
  trigger observed
- Dense wall-clock regression — cross-corpus seed reused 852,481/942,724 chunks and completed in
  11m22s; a current same-corpus repeat completed in 4.47s with nothing to embed

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

## Active Production Refresh Checkpoint

The retained live index has 359 manifest heads and 48,300 served blobs. The managed candidate has
491 heads and 55,080 served blobs. Hermetic same-commit conventional/managed parity passes, and the
candidate equals ripgrep, but literal live/candidate equality is impossible because the live scope
is stale and smaller.

The operator approved the scope expansion and production switch. The managed daemon is live; the
legacy corpus and shards remain available for rollback. The fixed binaries and 14:10 schedule are
installed, and a v2 cross-corpus seed is staged for incremental reuse. Phase 3 stays blocked until
the operator explicitly approves one production kickstart, it exits zero after warm reload, and
the post-refresh privacy/health checks pass.

---
*Phase: 02-managed-corpus-integration*
*Status: awaiting-production-refresh*
