---
phase: 05-source-provenance-format
plan: 01
type: execute
wave: 5
depends_on:
  - 04-01
files_modified:
  - internal/source/ref.go
  - internal/source/ref_test.go
  - internal/index/index.go
  - internal/index/builder.go
  - internal/index/build_target.go
  - internal/index/snapshot.go
  - internal/index/index_test.go
  - internal/index/snapshot_test.go
  - internal/diskstore/dedupstore.go
  - internal/diskstore/dedupstore_test.go
  - internal/diskstore/blobrefs_overflow_test.go
  - internal/blobstore/export_shared.go
  - internal/blobstore/export_deduped.go
  - internal/blobstore/refresh_deduped.go
  - internal/blobstore/compact.go
  - internal/blobstore/branch_refresh_test.go
  - internal/server/corpus.go
  - internal/server/dedup.go
  - internal/server/rankcorpus.go
  - internal/server/corpus_test.go
  - internal/server/rankcorpus_test.go
autonomous: true
requirements:
  - ADR-0020
user_setup: []
must_haves:
  truths:
    - Every in-memory and persisted file occurrence identifies project, commit, branch aliases, and relative path.
    - Non-default sources never claim an absolute path to bytes from the default checkout.
    - MOEDEX06 normalizes project/snapshot metadata and file refs point to source IDs instead of repeating branch strings.
    - MOEDEX03, MOEDEX04, and MOEDEX05 shards remain readable with explicit legacy provenance.
  artifacts:
    - path: internal/source/ref.go
      provides: Canonical project/snapshot/file source identity, display, equality, and navigation rules.
    - path: internal/index/index.go
      provides: Source-aware FileRef and compatibility AddFile wrapper.
    - path: internal/diskstore/dedupstore.go
      provides: MOEDEX06 writer/readers and legacy format dispatch.
    - path: internal/blobstore/export_shared.go
      provides: Manifest-v2 source occurrences preserved into served shards.
  key_links:
    - from: internal/blobstore/manifest.go
      to: internal/index/index.go
      via: Project/snapshot/file entries become source-aware index file refs without provenance flattening.
    - from: internal/index/snapshot.go
      to: internal/diskstore/dedupstore.go
      via: Snapshot/restore retain the full source graph through MOEDEX06 serialization.
    - from: internal/diskstore/dedupstore.go
      to: internal/server/corpus.go and internal/server/rankcorpus.go
      via: Eager and mmap loaders expose identical source metadata and content.
  prohibitions:
    - Never synthesize a non-default `AbsPath` from the default checkout root.
    - Never make new branch-aware writers emit MOEDEX03/04/05.
    - Never remove or silently reinterpret legacy readers in this phase.
---

<objective>
Create one source-provenance contract from CAS manifest through in-memory index and a new normalized
served-shard format.

Purpose: ensure deduplication never loses which project, branch, commit, and path produced a result,
and prevent non-checked-out content from claiming a misleading filesystem location.

Output: shared source types, source-aware index/build APIs, MOEDEX06 read/write support, branch-aware
export/refresh, server loading, legacy compatibility, and corruption/overflow coverage.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
</execution_context>

<context>
@docs/adr/0015-structured-context-result.md
@docs/adr/0017-lsp-navigation-and-the-serena-boundary.md
@docs/adr/0020-branch-aware-indexing.md
@docs/plans/phases/04-branch-aware-cas/PLAN.md
@docs/plans/phases/04-branch-aware-cas/SUMMARY.md
@internal/index/index.go
@internal/index/snapshot.go
@internal/diskstore/dedupstore.go
@internal/blobstore/export_shared.go
@internal/server/dedup.go
</context>

## Format contract and reversibility

- `source.Ref` separates `Project` metadata, `Snapshot` metadata, and file occurrence metadata.
- Compatibility fields `Repo`, `RelPath`, and valid `AbsPath` remain available during migration,
  but canonical identity/equality never depends on `AbsPath`.
- MOEDEX06 is content-less like MOEDEX05 and continues to use the shared content store.
- Expected MOEDEX06 layout is an 80-byte header with counts/offsets for project, snapshot, blob, and
  postings sections. Project rows store stable ID/namespace/leaf/default/worktree root once;
  snapshot rows store project index/commit/default flag/aliases once; blob file refs store snapshot
  ID, relative path, and Git OID.
- Existing v3/v4/v5 readers remain immutable compatibility paths. Rollback points the daemon back to
  the previous sibling shard directory; no downgrade writer is required.

## Artifacts this phase produces

- `internal/source.Project`, `Snapshot`, `Ref`, `IdentityKey`, `DisplayPath`, and `NavigablePath`.
- `index.AddSourceFile`; the existing `AddFile(repo, rel, abs, sha, content)` remains a legacy
  wrapper.
- Source-preserving `BlobData`, `Snapshot`, `Restore`, selective builder, and copy semantics.
- `MOEDEX06` writer, eager/mmap readers, strict bounds/corruption checks, and format fixtures.
- Branch-aware deduped full/delta export and server load support.

<tasks>

<task type="auto" id="5.1">
  <name>Task 1: Establish canonical source identity in memory</name>
  <files>internal/source/ref.go, internal/source/ref_test.go, internal/index/index.go, internal/index/builder.go, internal/index/build_target.go, internal/index/snapshot.go, internal/index/index_test.go, internal/index/snapshot_test.go</files>
  <read_first>internal/index/index.go, internal/index/builder.go, internal/index/snapshot.go, internal/ingest/source.go, internal/blobstore/manifest.go</read_first>
  <action>
Define immutable-style source values for numeric project ID, namespace, leaf label, default branch,
optional exact worktree root, commit, ordered aliases, default status, relative path, and Git OID.
Validate/sort aliases with default first. `IdentityKey` must distinguish project+commit+relative path
and must not use branch alone or filesystem path. `NavigablePath` returns a path only when the source
is the locked default commit represented by the configured checkout; non-default/unknown sources
return empty. Extend `FileRef` to carry canonical source data while retaining compatibility
`Repo/RelPath/AbsPath` access. Add `AddSourceFile`; make legacy `AddFile` construct explicit
legacy/default-unknown provenance. Thread full refs through builders, snapshots, restores, and
copy-on-snapshot tests.
  </action>
  <verify>go test ./internal/source ./internal/index -run 'Test.*(Source|FileRef|Snapshot|Restore|Legacy)' -count=1</verify>
  <acceptance_criteria>
    - Same content at two branches/paths retains two distinguishable refs without duplicating blob content.
    - Non-default and unknown-commit refs have no navigable absolute path.
    - Legacy AddFile callers preserve current Repo/RelPath/AbsPath behavior.
    - Snapshot copies are independent and restore reproduces source metadata and ordering exactly.
  </acceptance_criteria>
  <done>All in-memory paths can carry honest provenance while existing callers have an explicit compatibility bridge.</done>
</task>

<task type="auto" id="5.2">
  <name>Task 2: Implement normalized MOEDEX06 persistence</name>
  <files>internal/diskstore/dedupstore.go, internal/diskstore/dedupstore_test.go, internal/diskstore/blobrefs_overflow_test.go</files>
  <read_first>internal/diskstore/dedupstore.go, internal/diskstore/diskstore.go, internal/diskstore/blobrefs_overflow_test.go, internal/index/snapshot.go</read_first>
  <action>
Add `MOEDEX06`/format version 6 with explicit project-table, snapshot-table, blob, and postings
offsets/counts. Deduplicate projects by stable identity and snapshots by project+commit; serialize
aliases once per snapshot. File refs encode snapshot ID, relative path, and Git OID. Reconstruct a
worktree path only through `source.NavigablePath`. Reuse the MOEDEX05 shared content-store contract
and postings encoding. Extend format dispatch so `IsDeduped`, eager blob load, and mmap load accept
v5 or v6 while selecting the correct parser. Validate every count multiplication, section offset,
source ID, UTF-8/string length, alias/default invariant, and content-store key before allocation or
indexing. Preserve checked-in v3/v4/v5 fixtures and loaders.
  </action>
  <verify>go test ./internal/diskstore -run 'Test.*(V6|Deduped|Overflow|Corrupt|Legacy)' -count=1</verify>
  <acceptance_criteria>
    - A V6 round trip preserves full source values through eager and mmap paths.
    - Repeated project/commit/branch strings occur once in their normalized tables, not per file ref.
    - Invalid offsets/counts/source IDs/truncated strings/missing content keys return errors without panic or oversized allocation.
    - Existing V3/V4/V5 fixture tests pass without rewriting fixture bytes.
  </acceptance_criteria>
  <done>Branch-aware provenance has a bounded, normalized, backward-readable on-disk representation.</done>
</task>

<task type="auto" id="5.3">
  <name>Task 3: Preserve v2 manifest sources through full and delta export</name>
  <files>internal/blobstore/export_shared.go, internal/blobstore/export_deduped.go, internal/blobstore/refresh_deduped.go, internal/blobstore/compact.go, internal/blobstore/branch_refresh_test.go</files>
  <read_first>internal/blobstore/manifest.go, internal/blobstore/export_shared.go, internal/blobstore/export_deduped.go, internal/blobstore/refresh_deduped.go, internal/diskstore/dedupstore.go</read_first>
  <action>
Replay each v2 manifest file occurrence through `AddSourceFile` and write V6 for branch-aware data.
Preserve current shard-size boundary behavior at project/snapshot-safe boundaries and ensure one
content blob may collect refs from several shards/snapshots without losing any. Replace HEAD-only
served-delta classification with project/snapshot/source metadata so alias or namespace-only changes
rewrite affected ref metadata even when content keys do not change. Keep V1 -> V5 export behavior
unchanged. Update deduped compaction to read both V5 and V6 and copy all source tables. Full export
and delta refresh of the same final manifest must produce semantically identical loaded indexes.
  </action>
  <verify>go test ./internal/blobstore -run 'Test.*(V6Export|V6Refresh|MetadataOnly|FullDeltaParity|Compact)' -count=1</verify>
  <acceptance_criteria>
    - V2 branch manifests no longer hit the Phase 4 export barrier and always write V6.
    - V1 default-only manifests keep writing/reading their existing compatible format path.
    - Alias and namespace-only changes update loaded refs without adding content bytes.
    - Full and delta outputs expose identical source refs, content, and postings for the same manifest.
  </acceptance_criteria>
  <done>The served snapshot is a lossless projection of the branch-aware CAS manifest.</done>
</task>

<task type="auto" id="5.4">
  <name>Task 4: Load V6 through serving and invalidate provenance-bearing sidecars</name>
  <files>internal/server/corpus.go, internal/server/dedup.go, internal/server/rankcorpus.go, internal/server/corpus_test.go, internal/server/rankcorpus_test.go</files>
  <read_first>internal/server/corpus.go, internal/server/dedup.go, internal/server/rankcorpus.go, internal/diskstore/dedupstore.go</read_first>
  <action>
Teach exact and ranked corpus loaders to open mixed legacy and V6 shard directories only when the
directory manifest declares a compatible snapshot; reject accidental mixed-generation output.
Ensure eager and mmap corpus paths expose identical canonical refs. Include source-table bytes or a
canonical source digest in corpus/sidecar fingerprints so branch alias, commit, or project metadata
changes invalidate provenance-bearing caches even when blob content is unchanged. LSP/navigation
wiring must see only `NavigablePath`, never a synthesized non-default path. Add reload tests for V6
and legacy rollback directories.
  </action>
  <verify>go test ./internal/server -run 'Test.*(V6|Source|Fingerprint|Reload|Legacy)' -count=1</verify>
  <acceptance_criteria>
    - Exact and rank loaders return identical V6 source refs on eager and mmap paths.
    - Metadata-only source changes alter the corpus fingerprint and invalidate affected sidecars.
    - A V6 load never exposes an absolute path for a non-default snapshot.
    - Switching back to an unchanged V5 directory remains supported.
  </acceptance_criteria>
  <done>Persisted provenance survives load/reload and participates in every cache-freshness decision that can expose it.</done>
</task>

</tasks>

<verification>

- [ ] `go test ./internal/source ./internal/index ./internal/diskstore ./internal/blobstore ./internal/server -count=1`
- [ ] `make roundtrip`
- [ ] V3/V4/V5 checked-in fixtures load successfully.
- [ ] V6 eager and mmap round trips produce identical source identities.
- [ ] Corruption/overflow tests cover all four V6 sections and invalid cross-table IDs.
- [ ] `git diff --check`

</verification>

<success_criteria>

- Branch/project/commit provenance is lossless from manifest to loaded index.
- Content remains deduplicated while source metadata is normalized.
- Non-default results cannot be mistaken for checked-out filesystem files.
- Legacy shard rollback remains available without a migration command.

</success_criteria>

<output>
After execution, create `docs/plans/phases/05-source-provenance-format/SUMMARY.md` with the final V6
byte layout, compatibility matrix, corruption evidence, and rollback procedure.
</output>
