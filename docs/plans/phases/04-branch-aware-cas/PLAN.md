---
phase: 04-branch-aware-cas
plan: 01
type: execute
wave: 4
depends_on:
  - 03-01
files_modified:
  - internal/blobstore/manifest.go
  - internal/blobstore/build.go
  - internal/blobstore/compact.go
  - internal/blobstore/export_shared.go
  - internal/blobstore/refresh_deduped.go
  - internal/blobstore/manifest_test.go
  - internal/blobstore/branch_refresh_test.go
  - internal/blobstore/compact_test.go
  - internal/parity/manifest.go
  - cmd/moedex-index/main.go
  - cmd/moedex-index/main_test.go
autonomous: true
requirements:
  - ADR-0020
user_setup: []
must_haves:
  truths:
    - The CAS manifest represents project/commit snapshots, branch aliases, and ordered file occurrences without duplicating content bytes.
    - Refresh tree-walks only new project commits and reuses unchanged snapshot file lists exactly.
    - Branch deletion removes only source references; content remains live while any snapshot references it.
    - An ingest or lock ambiguity carries the prior complete project manifest and cannot publish a silent deletion.
  artifacts:
    - path: internal/blobstore/manifest.go
      provides: Version-2 project/snapshot/branch/file manifest plus version-1 reader compatibility.
    - path: internal/blobstore/build.go
      provides: Lock-to-manifest build and incremental project/snapshot delta application.
    - path: internal/blobstore/compact.go
      provides: Snapshot-union CAS liveness and crash-safe compaction.
    - path: cmd/moedex-index/main.go
      provides: Managed branch CAS build/refresh and machine-readable delta statistics.
  key_links:
    - from: .moedex/corpus.lock.json
      to: internal/blobstore/build.go
      via: Lock-v2 project/branch/commit state is the sole desired-state input for a managed refresh.
    - from: internal/ingest/tree.go
      to: internal/blobstore/manifest.go
      via: Each new commit yields ordered file occurrences keyed by normalized content hash.
    - from: internal/blobstore/manifest.go
      to: internal/blobstore/compact.go
      via: The union of every live snapshot's file content keys defines CAS liveness.
  prohibitions:
    - Never export a branch-aware manifest through MOEDEX05, which cannot persist branch provenance.
    - Never reinterpret or rewrite a version-1 CAS manifest in place as version 2.
    - Never reclaim a blob referenced by any live project snapshot.
---

<objective>
Advance the content-addressable store from one HEAD/file set per repository to an incremental,
branch-aware project-snapshot manifest.

Purpose: make branch refresh cost track changed snapshots and unique content while retaining the
failure/carry-forward guarantees of the current CAS.

Output: manifest v2, deterministic lock deltas, snapshot reuse, content-safe compaction, CLI stats,
and an explicit export barrier until MOEDEX06 exists.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
</execution_context>

<context>
@docs/adr/0004-content-addressable-blob-store.md
@docs/adr/0011-shard-level-freshness.md
@docs/adr/0020-branch-aware-indexing.md
@docs/plans/phases/03-branch-acquisition/PLAN.md
@docs/plans/phases/03-branch-acquisition/SUMMARY.md
@internal/blobstore/manifest.go
@internal/blobstore/build.go
@internal/blobstore/compact.go
@internal/blobstore/export_shared.go
</context>

## Scope and migration boundary

- Version-1 manifests remain readable and keep the existing default-only build/refresh behavior.
- Version-2 manifests are produced from a managed lock v2 into a new or explicitly rebuilt CAS
  directory; no implicit in-place conversion is allowed.
- Project identity is numeric project ID. Namespace rename is metadata-only.
- Commit snapshots own ordered file occurrences; branch names are aliases on snapshots.
- CAS content identity is the normalized content key, while Git OID remains acquisition metadata.
- Phase 4 may build and compact branch CAS data, but served export fails closed until Phase 5 can
  write source-aware shards.

## Artifacts this phase produces

- `BlobManifestVersion = 2` representation with `ProjectBlobs`, `SnapshotBlobs`, branch aliases, and
  ordered source file entries.
- `SnapshotDelta`/`ProjectDelta` classification for unchanged, added, alias-moved, removed,
  metadata-only, and carried-forward states.
- `BuildManagedCAS` and `RefreshManagedCAS` (or equivalent lock-driven entry points).
- Expanded `DeltaStats`: projects/branches/commits changed, tree walks, objects read, blobs/bytes
  added, refs added/removed, and projects carried forward.
- V1 compatibility tests, V2 incremental tests, and a MOEDEX05 branch-export rejection test.

<tasks>

<task type="auto" id="4.1">
  <name>Task 1: Define manifest v2 and explicit version compatibility</name>
  <files>internal/blobstore/manifest.go, internal/blobstore/manifest_test.go</files>
  <read_first>internal/blobstore/manifest.go, internal/corpus/lock.go, internal/ingest/source.go, internal/parity/manifest.go</read_first>
  <action>
Add a version-2 manifest organized as stable project ID -> commit snapshot -> ordered file
occurrences. Project metadata includes namespace and default branch. Snapshot metadata includes
commit, ordered aliases, default status, and files containing relative path, normalized content key,
Git OID, and tree mode. Enforce deterministic ordering and validate duplicate IDs, duplicate branch
ownership, commit/hash shape, path safety, default-branch presence, and stats consistency. Keep an
explicit v1 decode path that maps each legacy repo record to one default/unknown-project snapshot;
unknown versions fail closed. Do not write v2 over an existing v1 path except through an explicit
rebuild destination.
  </action>
  <verify>go test ./internal/blobstore -run 'TestBlobManifest(V1|V2|Validation|Ordering)' -count=1</verify>
  <acceptance_criteria>
    - V1 fixture bytes still load with their original repo/file/head semantics.
    - V2 round trips deterministically and rejects inconsistent alias/default/file state.
    - Two file occurrences may share one content key while retaining distinct paths and source snapshots.
    - Unknown manifest versions and mixed v1/v2 fields are errors.
  </acceptance_criteria>
  <done>The storage manifest can state branch provenance and content liveness without breaking legacy reads.</done>
</task>

<task type="auto" id="4.2">
  <name>Task 2: Implement lock-to-manifest delta refresh</name>
  <files>internal/blobstore/build.go, internal/blobstore/branch_refresh_test.go, internal/parity/manifest.go</files>
  <read_first>internal/blobstore/build.go, internal/blobstore/manifest.go, internal/corpus/lock.go, internal/ingest/tree.go, internal/ingest/catfile.go</read_first>
  <action>
Compare desired lock-v2 state to the prior v2 manifest by project ID, branch, and commit. Classify
unchanged snapshot, added commit, alias moved between already-known commits, removed alias/snapshot,
namespace-only rename, and failed project. Reuse unchanged snapshot file slices byte-for-byte; walk
and ingest only a commit not already present for that project. Register normalized content with
idempotent CAS `Put`. Apply a project's new manifest only after all of its required new snapshots
ingest successfully. On any project failure, copy the prior complete project record and mark the
aggregate refresh partial/non-zero while allowing independent projects to advance. Allow removals
only from a complete lock snapshot.
  </action>
  <verify>go test ./internal/blobstore -run 'TestManagedRefresh.*(Add|Alias|Move|Delete|ForcePush|Failure|Rename)' -count=1</verify>
  <acceptance_criteria>
    - Alias-only movement between known commits performs zero tree walks and zero object reads.
    - A force-push ingests only its new commit and adds only net-new normalized blobs.
    - A failed new snapshot leaves every prior branch result for that project represented in the output manifest.
    - Project rename changes metadata without re-reading content.
    - Refresh statistics reconcile exactly with the before/after manifests and CAS index.
  </acceptance_criteria>
  <done>Incremental work and removal semantics are explicit, measurable, and safe under partial failure.</done>
</task>

<task type="auto" id="4.3">
  <name>Task 3: Update compaction liveness and enforce the serving-format barrier</name>
  <files>internal/blobstore/compact.go, internal/blobstore/export_shared.go, internal/blobstore/refresh_deduped.go, internal/blobstore/compact_test.go, internal/blobstore/branch_refresh_test.go</files>
  <read_first>internal/blobstore/compact.go, internal/blobstore/export_shared.go, internal/blobstore/refresh_deduped.go, internal/diskstore/dedupstore.go</read_first>
  <action>
Compute live CAS keys as the union of every file in every live snapshot, independent of branch alias
count. Preserve current crash-safe sibling build/swap/recovery behavior. Teach export dispatch to
recognize v2: default-only v2 data may proceed only through a provenance-preserving adapter, while a
manifest containing non-default sources must return a clear `MOEDEX06 required` error until Phase 5
implements it. Never flatten snapshots into MOEDEX05 `Repo/RelPath/AbsPath` refs. Add tests where a
deleted branch shares content with another branch/repository and where an interrupted compaction
recovers to a complete old or new store.
  </action>
  <verify>go test ./internal/blobstore -run 'Test.*(Compact.*Snapshot|BranchExportBarrier|SharedLiveRef|Recovery)' -count=1</verify>
  <acceptance_criteria>
    - Compaction keeps every key referenced by any live snapshot and removes only proven-unreferenced keys.
    - Deleting one alias/snapshot cannot remove content still referenced elsewhere.
    - Branch-bearing v2 export cannot produce MOEDEX05 shards.
    - Recovery exposes a complete manifest/store pair after every injected swap boundary.
  </acceptance_criteria>
  <done>CAS garbage collection and export cannot erase or misrepresent branch-aware source state.</done>
</task>

<task type="auto" id="4.4">
  <name>Task 4: Expose managed CAS build, refresh, and honest stats</name>
  <files>cmd/moedex-index/main.go, cmd/moedex-index/main_test.go, internal/blobstore/build.go</files>
  <read_first>cmd/moedex-index/main.go, internal/blobstore/build.go, internal/blobstore/manifest.go</read_first>
  <action>
Route marked lock-v2 corpora through managed CAS build/refresh while preserving current v1/unmanaged
commands. Require an explicit new CAS destination for v1-to-v2 rebuild. Print stable human output
and optional JSON containing project/branch/commit changes, tree walks, object reads, blobs and
bytes added, source refs added/removed, carry-forwards, and failures. Return non-zero for partial
refresh after durably recording safe independent advances. Extend doctor/check to validate manifest
version, lock-to-manifest project coverage, content availability, and export eligibility.
  </action>
  <verify>go test ./cmd/moedex-index ./internal/blobstore -run 'Test.*(ManagedCAS|DeltaStats|ManifestDoctor)' -count=1</verify>
  <acceptance_criteria>
    - Existing unmanaged CAS commands and output remain compatible.
    - V1 input cannot be overwritten by an implicit v2 migration.
    - JSON stats sum to the actual manifest delta and identify every carried-forward project.
    - Doctor detects missing content keys, incomplete project coverage, and the pre-MOEDEX06 export barrier.
  </acceptance_criteria>
  <done>Operators can build, refresh, measure, and diagnose branch CAS state without serving lossy provenance.</done>
</task>

</tasks>

<verification>

- [ ] `go test ./internal/blobstore ./internal/parity ./cmd/moedex-index -count=1`
- [ ] `go test -race ./internal/blobstore -run 'TestManagedRefresh' -count=1`
- [ ] `make roundtrip`
- [ ] V1 fixtures remain readable and byte-compatible where writers are unchanged.
- [ ] A branch-bearing v2 manifest is rejected by MOEDEX05 export with an actionable error.
- [ ] `git diff --check`

</verification>

<success_criteria>

- Manifest v2 preserves every source occurrence and deduplicates normalized content.
- Delta work is proportional to new per-project commits, not raw branch count.
- Branch deletion and partial failure cannot remove still-live or prior-safe content.
- Phase 5 receives complete source metadata and an enforced no-loss export boundary.

</success_criteria>

<output>
After execution, create `docs/plans/phases/04-branch-aware-cas/SUMMARY.md` with manifest examples,
delta evidence, compatibility fixtures, and compaction recovery results.
</output>
