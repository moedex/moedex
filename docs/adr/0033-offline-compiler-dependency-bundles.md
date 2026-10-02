# ADR 0033: Offline compiler dependency bundles

- Status: Accepted for explicit local capture
- Date: 2026-09-30
- Extends: [0030](0030-managed-compiler-capture.md), [0032](0032-public-git-compiler-capture.md)

## Decision

Provide `semantic dependencies pack --packages DIR --output NEW_BUNDLE` to freeze
a caller-provisioned extracted NuGet global-packages tree. Packing does not
download, restore, or execute package contents. The new bundle contains
`manifest.json` and `packages/`; the versioned, deterministically ordered manifest
records relative file paths, sizes, SHA-256 digests, and executable flags.

`semantic capture --dependency-bundle BUNDLE --restore-offline` copies this
verified tree into the capture's private package cache. The original bundle is
never used as a writable build cache. Capture retains the exact manifest bytes
beside the projected source and checks the original and staged file rosters,
bytes, modes, and manifests around restore, extraction, and import. Mutation or
missing/unlisted files prevents artifact publication. Failure removes only owned
staging; existing destinations and caller inputs are preserved.

The optional `BuildContext.dependency_bundle_sha256` binds the exact manifest
bytes to each context ID before artifact construction. An omitted bundle leaves
existing context identities unchanged. This field is artifact audit evidence;
the compact format retains the context ID and artifact checksum without adding
another mmap field. The digest identifies the supplied cache, not a claim that
every package was used or that all SDK inputs came from the bundle. Actual used
references, analyzers and imports retain the worker's separately captured hashes.

## Limits and restore boundary

The initial bundle format permits at most 100,000 regular files, 1 GiB of file
content, a 16 MiB manifest, and 400,000 total directory entries. Symlinks, special
files, unsafe relative paths, duplicate entries, and output inside inputs are
rejected. Executable permission is normalized to private 0700 or 0600; ownership,
setuid and other filesystem permissions are not propagated.

Restore still uses explicit cleared package/fallback feeds, isolated home/temp
directories, and a private cache. Missing packages fail; the capture command does
not acquire missing dependencies or inherit the operator's package cache.
MSBuild targets and SDK resolvers execute trusted project code. This is not an
OS network sandbox, and source-controlled SDK resolver configuration can have its
own behavior. Dependency discovery/acquisition is a separate explicit operation
whose source URLs, archive hashes, and observed restore closure must be recorded.

## Restore evaluation compatibility

The optional `--restore-standard-evaluation` flag adds
`-p:RestoreUseStaticGraphEvaluation=false` to restore and reports that selection
in capture JSON. The default leaves the project’s choice intact. This is an
explicit operational evaluation choice, not a change to upstream source, target
framework, compilation configuration, or package versions. The selected Roslyn
SDK11 static-graph restore threw a null-reference exception; standard evaluation
succeeded with the same original project and feeds.

## Evidence

Tests cover deterministic packing, bounds, destination ownership, file and
manifest tampering, private-cache forwarding, context identity and round trips.
The selected real Roslyn project's acquisition and acceptance evidence is tracked
in [Roslyn compiler acceptance](../../research/semantic-intelligence/ROSLYN-COMPILER-ACCEPTANCE.md).
