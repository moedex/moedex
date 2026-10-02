# ADR 0026: Separate semantic identities, source occurrences, and retrieval scope

- Status: Accepted design; implementation is staged
- Date: 2026-09-30
- Program: [semantic intelligence](../plans/semantic-intelligence/PROGRAM.md)

## Context

Blob deduplication is a storage and retrieval advantage, but identical bytes can
mean different things in different projects. The same source file can bind an
interface to different packages, target frameworks, imports, or compiler options.
The existing graph key `(blob SHA, symbol offset)` identifies a declaration's
textual position. It does not establish a globally resolved symbol identity.
Similarly, selecting the first `FileRef` on a deduplicated blob can return the
wrong repository or path for a scoped request.

ADR 0020 proposes branch-aware source snapshots and occurrence provenance. This
decision uses that direction rather than defining another branch manifest.
ADR 0021's privacy filtering remains an ingestion boundary. ADR 0022 supplies
snapshot metadata and detects independently acquired rank/graph generations;
preventing a successful mixed-generation response requires a shared request
lease. ADR 0024 isolates compiler workspaces, and ADR 0025 requires compact,
mmap-friendly storage instead of expanding the graph into pointer-rich objects.

## Decision

### Three identities, with explicit implementation boundaries

1. **Content identity** remains the indexed blob SHA. Text, token postings,
   embeddings, and syntax extraction may share that content. A blob can have
   several source occurrences.
2. **Source occurrence identity** identifies repository/project, immutable source
   revision, relative path, span, and build context. Mutable branch names are
   aliases of source snapshots, not immutable identity. Existing `FileRef.Repo`
   is the current repository identifier; do not fabricate stable GitLab project
   IDs or commits that legacy artifacts do not contain.
3. **Qualified symbol identity** identifies language, package/project namespace,
   package version where applicable, and the compiler's qualified descriptor
   including signature/generic arity. Repository-local symbols need an explicit
   project/build namespace; a short display name or source offset is insufficient.
   One semantic symbol may have several declarations/occurrences, and one text
   occurrence may participate in several build contexts with different bindings.

These are accepted design concepts, not a claim that a new persisted semantic
symbol/occurrence format has shipped. The first S2 slice adds occurrence-aware
retrieval over existing file references and leased serving. Designing and
implementing a versioned semantic artifact follows the compiler/SCIP spike.
The existing graph IDs remain valid as content-position IDs; they must not be
silently reinterpreted as qualified symbol IDs.

### Cache keys reflect what an analysis depends on

Syntax cache identity includes blob content, detected language/parser mode,
extractor/schema version, and syntax-affecting options. Blob-only reuse is safe
only when those other inputs are fixed by the artifact's manifest.

Resolved-fact cache identity additionally includes source occurrence and project
identity, dependency bindings and versions, relevant project/configuration files,
compiler/toolchain and target framework, conditional symbols, generated inputs,
and resolver/rule versions. The privacy-policy fingerprint participates in
eligibility/publication identity. A dependency/configuration-only change can
invalidate semantic facts even when every affected source blob is unchanged.

Prefer content-addressed dependency manifests or digests over repeating these
inputs in every edge. Record them explicitly before claiming semantic refresh
equivalence. A compiler failure is not an empty successful result and must not
overwrite a previous valid artifact as if no dependencies existed.

### Evidence and provenance

A resolved relationship must carry source and target identity, the source
occurrence, exact byte spans into identified indexed content, extraction method
and version, binding method/status, and supporting evidence where a relationship
depends on configuration or another declaration. Evidence of a route string,
attribute, or DI registration is distinct from evidence that its target resolves.

Preserve ambiguous alternatives as diagnostic candidates. Distinguish resolved,
ambiguous, unresolved, unsupported, and failed/incomplete analysis in the eventual
adapter contract. Confidence describes the target binding supported by that
provenance; it is not a numerical assertion of runtime truth. Unknown commit or
configuration fields remain unknown rather than being filled from the current
checkout. Existing source spans and confidence tiers continue to serve the
syntax graph until this richer contract is implemented and migrated.

### Scoped retrieval over existing occurrences

`search_context` accepts optional `repo`, `path_prefix`, and `language` fields.
They are an intersection:

- `repo` matches the exact persisted repository identifier, without namespace
  inference or substring matching.
- `path_prefix` matches a literal repository-relative path or descendant at a
  slash segment boundary; an optional trailing slash has the same meaning.
  Absolute paths, backslashes, `.`/`..` components, empty interior components,
  and glob metacharacters are rejected rather than interpreted or normalized
  into a broader query.
- `language` uses the documented lowercase extension-based language categories.
  It describes eligible file occurrences, not successful compiler coverage.

Eligibility is evaluated before every ranking arm's candidate/top-K cap,
including dense selection. A deduplicated blob is eligible if at least one
occurrence matches, and returned provenance must select a matching occurrence.
An out-of-scope reference cannot be returned merely because its bytes equal an
eligible file. Empty scope preserves the unscoped compatibility path.

Graph-enriched scoped retrieval applies the same occurrence constraints to
anchors, traversal and neighbor selection before traversal/output caps. All
returned neighbor locations must match; filtering text after an unscoped walk
is insufficient. A graph annotator without scope support must report
`scope_unsupported` for scoped graph requests; `graph_depth=0` still permits
scoped source retrieval. Scope is a retrieval control, not an authorization or
privacy boundary. It does not replace ADR 0021.

### One lease for composed context

Production HTTP and stdio graph-enriched context acquire one immutable serving
bundle containing compatible rank, source and graph artifacts. Reload builds and
validates a replacement before publishing it, and releases the old bundle only
after its readers finish. Failure to load the replacement preserves the active
bundle. Data and snapshot metadata come from that lease. Standalone/custom
interfaces may retain ADR 0022's explicit mismatch rejection; they must not
silently return a cacheable mixed snapshot.

## Migration dependencies and acceptance

Scoped retrieval over existing `FileRef` needs no reinterpretation of persisted
graph node IDs. Its tests must cover duplicate bytes across repositories/paths,
out-of-scope decoys exceeding arm caps, invalid scope, and scoped neighbors.
Serving tests must pause a request across reload and verify reader lifetime,
failure recovery and one identity in both HTTP and stdio wiring.

Before introducing semantic storage, run the compiler/SCIP spike described in
[S2-PLAN](../plans/semantic-intelligence/S2-PLAN.md). Its evidence determines the
normalized descriptor and occurrence representation. Publish a versioned artifact
with explicit reader compatibility and rebuild behavior; retain legacy artifacts
as syntax-only rather than promoting old edges into semantic facts. Coordinate
source provenance with ADR 0020 before adding branch/commit fields to disk formats.
Prove configuration-only invalidation and refresh-versus-clean equivalence before
enabling semantic cache reuse in production.

## Consequences

Content remains shared while returned source locations become scope-correct.
Compiler facts can later distinguish equal text with different bindings. Scope
can reduce retrieval work and eliminate candidate starvation, but eligibility
checks add per-query work and must be measured across lexical and dense arms.
Leased bundles temporarily retain old and new mappings during reload; memory and
descriptor lifetime need explicit tests. Qualified IDs and dependency-aware
semantic artifacts remain planned work, not consequences inferred from passing
the scoped-retrieval gate.

## Related

The initial [compiler/SCIP experiment](../../research/semantic-intelligence/COMPARISON.md)
confirmed that generic arity and stable overload descriptors cannot be recovered
by treating this SCIP indexer's raw strings as Moedex identities. Direct Roslyn
provided the required labeled bindings and explicit incomplete outcomes on the
synthetic fixture. This supports the separation above; it does not accept a
production storage format or the experimental bounded project parser. Real build
evaluation and the remaining persistence/refresh gates are still required.

[0020](./0020-branch-aware-indexing.md),
[0021](./0021-ai-privacy-aware-indexing.md),
[0022](./0022-mcp-sdk-contract-and-snapshot-identity.md),
[0024](./0024-managed-lsp-workspace-isolation.md),
[0025](./0025-mmap-bm25-and-dense-sidecars.md).
