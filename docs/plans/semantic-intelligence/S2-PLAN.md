# S2: Scoped retrieval and semantic identity foundations

Status: first scope/serving slice locally implemented; semantic persistence pending.
The accepted architecture is
[ADR 0026](../../adr/0026-semantic-identities-and-scoped-retrieval.md). This plan
does not claim that persisted semantic identities or compiler extraction exist.

## First delivery slice

1. Add an explicit retrieval scope over the existing repository/path/language
   occurrences. Apply it before lexical, symbol, fuzzy and dense arm caps and
   select only eligible references on shared blobs. Expose `repo`, `path_prefix`
   and `language` through `search_context`, with validated literal path semantics.
2. Apply scope to graph anchors, reachable nodes and returned neighbor locations
   before caps. Preserve diagnostic confidence and evidence; unsupported custom
   annotators fail explicitly rather than silently widening the scope.
3. Use one refcounted rank-plus-graph serving bundle for production HTTP/stdio
   enriched context. Validate replacements before publication; preserve prior
   readers and active state when replacement loading fails.
4. Exercise source bytes, real deduped shards, graph extraction and HTTP MCP in
   `internal/eval/scoped_context_test.go`. Retain focused unit tests for arm-level
   filtering and dense eligibility, which an HTTP lexical fixture cannot prove.

Acceptance: matching occurrence survives content dedup; more than top-K outside
decoys cannot starve inside results; path segment boundaries and language filters
are respected; invalid scopes are rejected; neighbors expose only scoped
locations; default behavior remains compatible; reader/reload race tests pass.
These results establish retrieval and serving behavior, not semantic precision.

## Compiler/SCIP spike before semantic persistence

Initial fixture experiment completed locally on 2026-09-30:
[comparison and implementation direction](../../../research/semantic-intelligence/COMPARISON.md).
The direct Roslyn subset matched 19 reference/status labels and five identity
groups; SCIP exposed descriptor stability/arity and completeness gaps. This
supports a compiler worker with Moedex-owned identities, not adoption of the
experimental project parser or raw SCIP IDs. The broader acceptance work below
remains pending for real MSBuild, packages, generators and incremental indexing.

Use pinned public or otherwise authorized repositories plus small source-authored
fixtures; isolate analyzers in ADR 0024 projections. Compare normalized output
from a direct Roslyn adapter and a SCIP importer on the same C# source snapshots.
Do not choose a format merely because one tool can emit it. The comparison must
include overloaded and generic methods, nested types, partial declarations,
interface calls, project/package references, conditional compilation, generated
sources, unresolved dependencies and two projects sharing identical source bytes
but binding the same name differently.

Record toolchain, indexer, schema and configuration versions; exact source and
dependency identities; language/project coverage; declaration/reference/relationship
counts; unresolved and ambiguous counts; warm/cold time, peak memory and artifact
size. Gold relationships are reviewed from source before inspecting tool output.
The spike must answer:

- Can descriptors distinguish overloads/build namespaces and connect package
  references without short-name guesses? Which relationships remain adapter work?
- Do occurrence locations round-trip to exact bytes and source context, including
  shared content and generated files? Are incomplete projects explicit?
- Can dependency/configuration-only changes invalidate precisely enough to avoid
  stale facts? Does incremental output match clean indexing after add, delete,
  rename, package upgrade and conditional-symbol changes?
- Can output be streamed into bounded, deterministic normalized records without
  retaining the compiler graph in the pure-Go serving process?
- Does a failed compiler/indexer preserve useful lexical retrieval and avoid
  publishing a falsely complete semantic snapshot?

Accept an adapter only after reproducible evidence for these questions. Record
unsupported cases; no precision percentage, scalability target or CodeGraph win
is implied by tool availability. Keep native reference/relationship output for
audit and compare deterministic normalized records, not incidental compiler IDs.
Continue domain adapters separately: SCIP or compiler references alone do not
establish configured HTTP destinations, message queues or ORM table bindings.

## Deferred dependent delivery

The spike informs a versioned qualified-symbol/occurrence artifact and provenance
schema. Implement reader/writer round-trips, corruption validation, immutable
publication and cache migration before serving those IDs. Keep existing
content-position graph IDs explicitly separate. Integrate commit/branch fields
with ADR 0020's source-snapshot schema rather than creating a competing manifest.
Introduce dependency-aware semantic refresh only with clean-rebuild equivalence
and failure/publication tests.

S2 remains incomplete until its declared persistence and shared-content/different-
binding gates are delivered. The initial scope/bundle slice is useful independently
and must be reported as such. Compiler-backed quality, private-corpus task results,
and a paired CodeGraph runtime comparison remain separate pending gates.

## Delivered compiler/artifact foundation (2026-09-30)

[ADR 0027](../../adr/0027-compiler-worker-and-semantic-artifact.md) records the
implemented MSBuild worker, validating Go importer, and bounded immutable offline
artifact. Qualified symbols remain separate from context-specific occurrences and
explicit binding statuses. Shared physical source with different compiler targets
now round-trips through the artifact, including real worker captures. Generated
bytes and compiler diagnostics are retained. Failed, incomplete and truncated
captures cannot replace a staged artifact or publish a generation.

This delivers the offline persistence foundation, not the complete S2 serving gate.
The existing snapshot transaction now accepts an explicit offline semantic component
under [ADR 0028](../../adr/0028-semantic-snapshot-attachments.md), rechecking recorded
inputs and exact indexed source membership. Resolution verifies bounded attachment
integrity; omission on the next build drops semantic evidence. The compact mapped
representation and exact compiler binding/definition tools are now implemented
under [ADR 0029](../../adr/0029-compiler-lookup-index-and-tools.md). Managed compiler
execution and real-project acceptance remain prerequisites to automatic indexing.
See DELIVERY.md for validation and limitations.
