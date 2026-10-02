# ADR 0028: Explicit offline semantic snapshot attachments

- Status: Accepted
- Date: 2026-09-30
- Predecessor: [ADR 0027](./0027-compiler-worker-and-semantic-artifact.md)

## Decision

`moedex index snapshot build -semantic-artifact FILE` can attach one complete
compiler artifact to a newly built index generation. Publication uses the existing
snapshot transaction. The artifact is copied into `semantic/artifact.json`, outside
the `serve` tree, and described by a separate `semantic` component. It remains
offline evidence; no compiler query or graph capability is advertised.

The optional flag is explicit on every build. Omitting it produces a generation
without semantic evidence. Failure aborts the new transaction and preserves
`CURRENT`; the builder never borrows an attachment from an earlier generation.

## Admission

The builder validates the artifact and requires complete recorded compiler
contexts from the supported MSBuild worker. It matches repository namespaces to
the build's discovered repositories and checks supplied project IDs and commits
against their recorded identities. A supplied commit remains a caller assertion,
not proof that the compiler ran in a clean worktree.

`semanticimport.Revalidate` checks capture identity, source and dependency
membership, raw source bytes and recorded source-scoped build inputs through
confined filesystem roots. This includes project files, imports and restore
assets. Genuine virtual generated sources retain their embedded bytes; physical
generated files are also checked on disk.

Every ordinary compiler source must match an exact repository/path occurrence in
the newly built lexical shards. Matching content in a different file or repository
is insufficient. The comparison permits exactly ingestion's removal of a leading
UTF-8 BOM; compiler spans remain raw-byte spans and are not rewritten into search
offsets. Indexed captured build inputs are compared too. Generated sources and
derived build inputs absent from lexical shards remain captured-only evidence.
All captured project files must be indexed.

Diagnostics and generated content can derive from inputs beyond indexed source
files. Until sanitized compiler projections have explicit provenance, attachment
requires the existing whole-workspace privacy eligibility rule: no level-1 input
restriction anywhere in the repository. The effective policy must match ingestion
before and after admission. Its aggregate fingerprint is recorded in component
metadata. This does not authorize future compiler execution on arbitrary projects.

## Identity and loading

The component's `InputFingerprint` equals the snapshot manifest's
`CorpusFingerprint`, whose domain hashes serving artifacts. This domain differs
from the legacy rank/graph fingerprint; they are never compared as equivalent.
Metadata records `coverage=explicit-project-subset` and `usage=offline-only`.

Snapshot validation reserves the component name, format and artifact path.
Resolution verifies the optional file's size and SHA256 with a confined streaming
read, bounded to one 64 MiB artifact. It does not deserialize semantic JSON into
the warm serving heap or rehash the entire corpus on each reload. An absent
component is normal. A missing, corrupt or incompatible advertised component
prevents the replacement generation from being opened. Existing readers retain
their prior generation until normal retirement.

Checksums bind the attachment to the manifest; they do not authenticate a
maliciously rewritten manifest or independently attest compiler truth. Explicit
legacy shard-directory access does not acquire or claim semantic attachments.

## Limits and next steps

Admission rechecks **recorded inputs**, not a new compiler evaluation. It does not
discover newly added wildcard inputs, reconstruct generator environment reads,
or attest the current SDK/external dependencies. These remain historical captured
contexts associated with matching indexed bytes, not a guarantee of current
semantic completeness. Concurrent mutable worktrees are not immutable acquisition
snapshots. Automated refresh requires managed compiler execution and clean-build
equivalence before it can claim freshness.

The attachment and import bounds are not build-process RSS bounds: matching
lexical input uses the existing per-shard blob loader. Warm semantic retrieval
still requires a compact indexed representation, an explicit raw-to-normalized
source bridge, query integration, and scale measurements under ADR 0025. Package
identity normalization, multi-context merging and domain adapters remain separate
work. No competitive quality or production deployment claim follows from this
publication boundary.
