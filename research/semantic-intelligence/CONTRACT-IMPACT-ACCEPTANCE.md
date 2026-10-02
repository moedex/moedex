# Scoped compiler contract impact

Recorded October 1, 2026. Locally implemented and verified; not deployed.
See [ADR 0038](../../docs/adr/0038-scoped-compiler-contract-impact.md).

## Delivered behavior

`compiler_contract_impact` retrieves recorded framework observations for an exact
qualified domain target symbol. An initial call returns bounded context choices;
paths require explicit selection of up to 32 contexts. Each path carries the
contract, matched target roles, framework fact, source occurrence, compiler/build
context, raw source hash, and optional declared owner. Multiple matching roles in
one fact produce one observation. Different types with the same spelling remain
separate. Multiple variants of one repository/project cannot be selected together.

Semantic index v3 persists reverse target postings ordered by target, context,
source fact, domain fact, and target position. Queries seek selected contexts
without scanning other contexts. Context discovery skips directly between context
groups. The result limit is 100, with a defensive 10,000-posting work ceiling and
the existing conservative 2 MiB materialization budget. Current fact-role limits
usually make the result limit bind first. Byte overflow fails the query; result
and work limits report truncation. These are not process-memory or serialized
MCP response-size limits. Returned evidence owns its strings and uses existing
snapshot leases.

Existing v1/v2 indexes retain binding/definition reads and explicitly report
`index_upgrade_required` for impact queries. A genuine pre-change v2 fixture
checks compatibility. Index validation checks exact posting cardinality, strictly
unique ordering, and correspondence to every recorded target; checksum-valid
missing, duplicate, or relabelled postings are rejected.

## Independent real compiler gate

The [fixture](../../internal/eval/testdata/semantic-intelligence/contract-impact-v1/README.md)
contains six projects, compiled in Debug and Release with .NET SDK 10.0.100 and
real pinned MassTransit/EF metadata. Each capture contains five framework facts.
The shared contract has publisher, consumer, and storage observations; a separate
project deliberately declares a different type with the same qualified spelling.

Eleven production MCP handler queries exercise context discovery, discovery and
path truncation, three selected projects yielding four observations, each publisher
variant separately, distractor identity separation, an empty mismatched scope,
and conflicting, duplicate, and unknown selections. Assertions check exact source
spans, hashes, identities, owners, table values, and artifact provenance after
artifact round-trip and index reopen. Structured response hashes are recorded.

The multi-configuration gate combines captures with a test-only artifact union
helper. This is not a production multi-capture merge command or deployed daemon
trial. A single project-graph capture already contains its referenced projects;
independent captures still need an explicit publication/composition workflow.

## Validation and recorded evidence

Independent review found and corrected a publication blocker inherited from the
framework-fact change: retained-input validation still accepted only worker
version 1. It now admits versions 1 and 2, rejects unsupported versions, and
continues rejecting source drift. Both real captured configurations pass retained
input validation. A separate snapshot-build regression publishes a version 2
worker-shaped artifact and reopens its derived index through the production
attachment path. This separates real-capture validation from synthetic publication
coverage; it is not a claim of a deployed multi-capture snapshot.

Full Go tests, vet, and build pass. Race checks pass for semantic artifacts,
import/index, MCP, snapshots, snapshot building, and serving. Tagged LSP compiler
checks and the previous real framework-domain gate also pass. A short index-open
fuzz smoke passes; it is not an exhaustive fuzz campaign. Targeted query tests
include more than 10,000 references in one context, direct discovery of a second
context, sparse selected-context lookup, role coalescing, result truncation,
materialization failure, cancellation, and owned results after index close.

The [machine-readable record](results/contract-impact-20261001/result.json)
contains hashes for both raw compiler streams, source-authored labels, package
identities, final implementation sources, normal/race query logs, and validation
logs. The permanent 3,276-byte legacy index fixture is independently checksummed.
Compiler/package binaries and restored projections remain outside the repository.

## Scope

Results are compile-time observations, not runtime publisher-to-consumer delivery,
service activation, database activity, or proof that selected projects share a
compatible dependency closure. Missing supported facts do not prove absence of
impact. Private wrappers still require verified adapters. Fixtures against real
framework metadata establish behavior, not held-out application quality or
competitive superiority. Large Roslyn lexical and syntax-graph gates are unchanged
and were not rerun for this compiler-query slice.

The next delivery step is production capture composition/publication with retained
input validation, followed by independently labelled application impact tasks.

The production composition/publication follow-up is now recorded in the [composition acceptance](COMPOSITION-ACCEPTANCE.md).
