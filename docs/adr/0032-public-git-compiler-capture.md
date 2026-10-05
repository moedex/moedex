# ADR 0032: Public Git compiler capture

- Status: Accepted for explicit local capture
- Date: 2026-09-30
- Extends: [0030](0030-managed-compiler-capture.md)

## Decision

Allow `semantic capture --checkout PATH --commit FULL_OBJECT_ID --origin HTTPS_URL`
as an alternative to `--managed-root`. Managed capture continues deriving identity
from its acquisition lock and rejects caller-supplied commit/origin overrides.
Public capture requires checkout root, HEAD and configured origin to match the
explicit selection. Origin is recorded audit evidence, not proof of remote
ownership or network verification. No synthetic GitLab catalog or project ID is
created.

Both paths share exact Git-blob projection and existing compiler admission checks.
Public capture additionally verifies canonical tracked bytes against the committed
inventory before and after execution; Git status flags and content filters cannot
hide source drift. Regular untracked build outputs do not enter the projection.
Symlinks and submodules remain unsupported. Privacy checks apply to canonical and
projected sources. Destinations must be new and outside the source checkout.

`--max-projection-bytes` explicitly selects a bounded input budget: zero retains
the 256 MiB default and values above 1 GiB are rejected. This does not increase
worker-output, artifact, or compact-index limits. Whole Roslyn source needs an
explicit 512 MiB projection budget; a successful projection is not compilation.

The optional `SourceSnapshot.origin` field is included in snapshot identity and
artifact integrity. Omitting it preserves existing artifact identities. Origin
is audit-only and is not added to the compact mmap record or MCP query schema.
Public project ID is absent from the artifact (the CLI summary reports zero).
For attachment through current unmanaged ingestion, `--repo` must match the
checkout directory basename.

## Consequences and evidence

The existing package-free, empty-feed restore remains the execution contract.
This change does not acquire SDKs or dependencies. Roslyn's original SDK/Arcade
requirements still need a separate reproducible dependency-staging design.

Public capture is checked with synthetic fixtures and original public sources.
See [Roslyn compiler acceptance](../../research/semantic-intelligence/ROSLYN-COMPILER-ACCEPTANCE.md).
