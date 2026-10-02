# ADR 0039: Explicit compiler capture composition

Status: Accepted; locally implemented and verified.

## Context

Contract-impact queries can span captured projects, but separate capture files
previously required a test-only union. Captures can overlap, retain different
variants, or come from different repositories. Combining them must preserve their
identities and reject contradictory evidence. Retained compiler projections can
also differ from the corpus checkout used to build lexical shards.

## Decision

Add `semantic compose` to stage a new complete immutable artifact from explicit
input artifacts. Union records by exact content identity; reject contradictory
records and conflicting bindings rather than selecting a winner. Preserve build
contexts and their dependency references. Sort output records deterministically,
deduplicate exact repeats, and retain independent variants. Composition does not
assert dependency-closure compatibility or runtime topology.

Bound both aggregate encoded inputs and output to 64 MiB, with at most 32 inputs.
A caller may lower the byte limit. Inputs must independently validate and be
complete; composition is not a repair mechanism for missing dependencies.
Staging does not change CURRENT or overwrite an existing file.

Publication uses the existing snapshot transaction and `--semantic-artifact`.
For separate retained projections, `--semantic-workspaces FILE` accepts a bounded
JSON object mapping every source snapshot ID to a directory. Duplicate, missing,
and unknown keys are rejected. Relative directories resolve beside the map file.
This local map is not persisted as semantic provenance. The existing single-root
option remains supported and mutually exclusive with the map.

Each mapped root undergoes the existing source/configuration revalidation and
privacy checks. Recorded source bytes and indexed configuration must still agree
with the corpus being published. Composition may retain historical alternatives
that cannot be attached to one current corpus generation; publication fails
without changing CURRENT in that case. Optional inputs that are absent from lexical shards may differ between retained
projections, such as generated editorconfig files containing build paths. Each
variant still undergoes retained-input validation. If an optional path is indexed,
all recorded variants must match its bytes. Required sources/project files cannot
have conflicting variants.

Mapping a retained root never relaxes the
commit, repository, project, indexed-byte, or privacy requirements.

## Validation

Test exact deduplication, input-order determinism, owned records, malformed and
incomplete inputs, contradictory evidence, bounds, cancellation, and exclusive
output. Exercise composed publication and rollback with separate retained roots,
then query exact scoped identities through compiler MCP. Reuse real independently
labelled Debug/Release compiler captures through the production composition API.

## Related

- [Scoped contract impact](0038-scoped-compiler-contract-impact.md)
- [Snapshot attachments](0028-semantic-snapshot-attachments.md)

- [Composition acceptance](../../research/semantic-intelligence/COMPOSITION-ACCEPTANCE.md)
