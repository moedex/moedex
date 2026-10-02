# ADR 0035: Persist shared target sets and source evidence

Status: accepted, implemented locally

## Context

The Roslyn follow-up to ADR 0034 completed preparation in 19.009 seconds but
retained 182.6 million candidate pairs before deduplication. A second aggregation
copy brought sampled physical footprint to 66.8 GB. Faster preparation and
removing that copy would not eliminate the underlying Cartesian product.

## Decision

Persist each ordered target set once and record source evidence by reference to
that set. A source record retains its graph node, edge type, confidence,
evidence range, name, generation, and source-specific self exclusion. Candidate
and Pattern relationships both use this representation. Pattern repository
filtering selects a shared eligible set; it does not promote ambiguous matches.
Compiler/LSP-confirmed and whole-corpus facts can remain explicit edges.

Self exclusion applies to physical shard/blob/offset identity before content
deduplication. An identical definition in another shard can still produce a
content-addressed self edge. If excluding the first physical copy changes the
first surviving target order, a constant-size single-target move descriptor
records that order. Identical source records retain the first source's order,
as the original emitter did. Target lists are not copied per source.

Diskgraph format 4 atomically contains a validated physical version-3 adjacency
payload, shared target sets, and factored-record metadata. Explicit and
factored records preserve their interleaved insertion order. Version-3 graphs
remain readable; graphs without factored records can retain that format.
Corrupt section extents, target identities, exclusions, ordering metadata,
prototype witnesses, and logical counts are rejected on open.

`NumEdges`, `NodeAt`, `EdgeAt`, `Edges`, and `EachEdge` retain their logical
relationship semantics. Logical random access uses metadata proportional to
stored groups, not expanded relationships. `NumRecords` reports physical source
records separately. Existing materializing APIs remain compatibility surfaces;
build, refresh, serving catalogs, and schema counts use physical-record iteration
and shared-set folds. The optional persisted call-audit path remains exhaustive
and can still materialize large Pattern target populations.

Build and refresh share the compact pipeline. Refresh carries shared target
sets and source evidence without decoding every logical pair. LSP reconciliation
prunes complete verified call-site groups before adding confirmed facts;
default builds avoid constructing LSP-only metadata.

Catalog construction visits shared endpoints once. Schema and build counts
fold physical records arithmetically. Forward traversal expands only requested
adjacency; reverse traversal intersects requested targets with shared sets and
scans physical source records. Reverse selection caching has explicit entry,
payload, and match limits with exact streaming fallback. It is not a persisted
reverse index or a promise of constant-time reverse lookup.

Serving retains at most 10,000 qualifying expanded edges per graph traversal.
Results that exceed this budget explicitly report `truncated`,
`total_is_exact=false`, and the expansion limit. Neighbor bucket totals are
then lower bounds. Stored candidates remain complete; an output budget never
changes persistence or pretends the result was exhaustive. Low-level logical
iteration and random access remain available for exhaustive consumers.

Clustering computes exact logical eligibility and endpoint counts before
expansion. Its existing node cap is supplemented by a default 1,000,000-edge
work cap, configurable through `MOEDEX_GRAPH_CLUSTER_MAX_EDGES`. Exceeding it
persists an `over_edge_cap` envelope with counts and no partial communities.
Cluster sidecar version 3 carries the edge cap; callers receive explicit
unavailability rather than a misleading available result.

## Consequences

Physical storage follows source evidence and shared target sets instead of
source/definition pairs. Exact repository scopes may require more than one
target set for a name. Large logical graphs can still be expensive to traverse
or cluster, so those operations retain separate, observable work budgets.
Many overlapping repository-context sets can multiply target-roster storage;
the single-repository Roslyn gate does not measure that case.

Preparation caches, builders, catalogs, mmap pages, and serialization buffers
still consume memory. This format is not a total process memory bound. The
external corpus runner now flushes live logs and enforces sampled memory,
deadline, and output limits. Its macOS metric is physical footprint, while
Linux uses RSS; it monitors the direct child rather than optional subprocesses.

## Evidence

An independent 24-seed publication oracle compares complete persisted records
and node order with the original expanded generator and emitter. It covers
colliding names, shared content, repository filters, unknown repositories,
quoted source, and physical self exclusion. It caught the target-order issue
before corpus testing. Compact carry versus clean rebuild, optional LSP
reconciliation, disk corruption, logical random access, confidence upgrades,
query parity, cancellation, truthful truncation, and cluster admission have
focused regression coverage.

Full-corpus acceptance and resource measurements are recorded separately from
unit correctness. A stored graph pass does not imply unrestricted clustering,
compiler precision/recall, or competitive parity with CodeGraph.

## Related

- [ADR 0034](0034-source-first-graph-candidates.md)
- [Previous Roslyn graph trial](../../research/semantic-intelligence/ROSLYN-GRAPH-ACCEPTANCE.md)
- [Compact Roslyn acceptance](../../research/semantic-intelligence/ROSLYN-FACTORED-GRAPH-ACCEPTANCE.md)
- [Ordering-preserving latency follow-up](../../research/semantic-intelligence/ROSLYN-LATENCY-ACCEPTANCE.md)
- [Exact C# extraction and startup follow-up](../../research/semantic-intelligence/ROSLYN-EXTRACTION-ACCEPTANCE.md)
