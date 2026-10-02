# ADR 0027: Compiler worker and bounded semantic artifact foundation

- Status: Accepted; serving integration implemented in [ADR 0029](./0029-compiler-lookup-index-and-tools.md)
- Date: 2026-09-30
- Predecessor: [ADR 0026](./0026-semantic-identities-and-scoped-retrieval.md)

## Decision

Run C# compiler extraction in a separate process using actual MSBuildWorkspace
project evaluation. Admit its output through a validating Go importer and write a
versioned, immutable semantic artifact. The existing syntax graph, index formats
and serving response IDs retain their meanings. This artifact is not automatically
loaded or published by the search daemon.

The [compiler comparison](../../research/semantic-intelligence/COMPARISON.md)
showed that qualified compiler descriptors preserve overload signatures and generic
arity that the tested SCIP indexer did not reliably preserve across changes.
SCIP remains an interchange possibility; compiler output is input to Moedex's
identity contract, not its persisted key format.

## Worker boundary

`tools/semantic-dotnet` loads one restored project and its project references for
an explicitly selected configuration and target framework. The first tested SDK
is 10.0.100; workspace/compiler/build-host dependencies come from that SDK's
bundled dotnet-format installation. The worker does not restore packages itself.
It runs on a disposable source projection. MSBuild targets and analyzers/generators
execute code; this process is not a sandbox and does not implement the future
managed projection/resource-lifecycle coordinator.

Each project emits the actual compiler source roster and options, evaluated
project/import/package-assets identities, metadata/project references (including
aliases), analyzer inputs and transitive dependency context hashes. Exact
`capture_json` UTF-8 bytes determine the build-context fingerprint. Observed file
inputs are rechecked before the final stream summary. This detects observed input
drift; it does not attest to every filesystem/environment read made by custom
build tasks or generators, nor make a changing source tree transactional.

Workspace and compilation failures remain explicit. Generator verification uses
the workspace's generator inputs in a separate public driver pass, records its
diagnostics, and checks generated outputs against the workspace. Generator failure
or disagreement makes analysis incomplete. This runs generators twice; resource
and determinism costs are part of future real-corpus acceptance.

Physical source paths must remain within the projection. Genuine virtual generated
sources receive reserved relative paths and embedded UTF-8 bytes. Exact raw source
bytes, BOMs and line endings define spans. Non-UTF-8 input fails explicitly. No
raw compiler span is relabeled as an offset into BOM-normalized search content.

The versioned JSONL stream ends with a terminal summary carrying project and
occurrence counts. Truncated output has no successful interpretation. Exit 2
retains a structurally complete but incomplete capture; exit 1 has no valid terminal
summary. Neither output is eligible for the complete-artifact staging command.

## Identity and provenance

`internal/semantic` separates:

- **Source snapshot:** explicit repository namespace, optional known acquisition
  project ID/commit, and a fingerprint of the observed source roster. Unknown
  provenance stays absent. Caller-supplied commit metadata is not independently
  attested by the importer. Generated inputs are labeled and retained separately
  from ordinary source bytes; a commit does not make them Git objects.
- **Qualified symbol:** language, project/assembly/package namespace, descriptor,
  and descriptor kind. Its ID excludes source content and build-context digests.
  Compiler documentation descriptors retain signatures and generic arity. Local
  `location_fallback` descriptors are explicitly not guaranteed edit-stable.
- **Build context:** project, exact captured input manifest/fingerprint, extractor
  version, source membership, referenced contexts, completeness and issues.
- **Occurrence:** source, context, role, reference kind and raw UTF-8 byte span.
  Reference kind distinguishes a constructor and a type reference at the same
  token. Identical bytes in distinct contexts retain distinct occurrences.
- **Binding:** resolved, ambiguous, unresolved or unsupported status; qualified
  target or diagnostic candidates; extractor/method and optional enclosing symbol.
  Enclosing evidence is retained without asserting runtime dispatch or creating
  CALLS edges. Compiler diagnostics are retained with context and source location
  when available.

The C# worker currently uses repository plus project path for source namespaces,
and actual assembly identity for external namespaces. NuGet package-to-assembly
identity normalization is not delivered. The generic store can represent package
namespaces, but this importer does not invent them. ADR 0020's future branch-aware
source schema remains authoritative; no parallel branch manifest is introduced.

## Admission and storage

`internal/semanticimport` verifies worker schema, project/context association,
raw source hashes/sizes/text spans, captured source rosters, supplied redundant
provenance, diagnostics, dependency status and terminal counts. A confined Go
filesystem root prevents source-resolution escapes. Locally captured build-input
files (project files, imports and assets) are also rehashed at admission; SDK and
external input hashes remain recorded evidence rather than independently attested
installation state. Embedded generated bytes survive in the artifact.

The first format is a bounded offline JSON artifact with a SHA256-protected,
base64-encoded payload. Readers reject unknown formats/versions/fields, trailing
JSON, corruption, invalid IDs, duplicate records and broken cross-references.
All ordinary source byte authenticity checks happen at import; generated source
bytes are checked again by the reader. SHA256 provides integrity detection, not
an authenticated signature or proof of compiler truth.

The maximum encoded artifact size is 64 MiB. Import separately bounds the wire
stream, source bytes and locally checked build-input bytes. Decoding materializes
records on the heap; those byte limits are not an exact RSS ceiling. This is **not
ADR 0025's eventual corpus-scale mmap serving index**. Scale acceptance and a
compact serving representation remain prerequisites to loading semantic artifacts
in warm retrieval.

`semantic.Write` validates, fsyncs, and exclusively links a temporary file into a
caller-owned staging directory. It never replaces an existing artifact. Library
APIs can retain incomplete artifacts for diagnostics; the CLI admits only complete
captures. `moedex semantic import` stages a new file, and `semantic inspect`
validates and summarizes it. Neither command changes `CURRENT`. The explicit
snapshot-builder attachment added in [ADR 0028](./0028-semantic-snapshot-attachments.md)
lists the artifact as a component in the existing `internal/snapshot` transaction;
no second generation publisher is introduced.

## Supported slice and acceptance

One imported stream currently contains one configuration/framework context per
project plus its loaded dependencies. Separate configurations require separate
invocations; multi-context merging is not implemented. Capture is full extraction,
not incremental reuse. Default Go builds and tests do not require .NET.

Acceptance for this slice covers real MSBuild source-to-artifact round trips,
shared-source binding separation, all existing 19 compiler labels, raw BOM/Unicode
spans, deterministic captures, configuration sensitivity, diagnostics, generated
source success/failure, corruption/size limits and preservation of an existing
staged artifact after failed/incomplete imports. It does not establish private
corpus quality, production deployment, complete C# reference recall, package
upgrade correctness or a CodeGraph runtime win.

Next: managed projection/process isolation and resource limits; real-project,
package and multi-target cases; explicit package identities; compact persisted
indexes and snapshot component integration; semantic query fusion; and
incremental-versus-clean equivalence across source/dependency/configuration changes.
