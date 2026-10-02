# ADR 0038: Scoped recorded compiler contract impact

Status: Accepted; locally implemented and verified.

## Context

Recorded framework facts expose typed contracts at individual source positions.
Agents also need to ask which captured producers, consumers, registrations, and
storage declarations mention a chosen contract. A short-name join conflates
unrelated symbols; an unscoped join conflates build variants. Scanning every
compiler fact on each query would give neither predictable work nor useful
interactive behavior on larger captures.

## Decision

Add `compiler_contract_impact` over a persisted reverse index of exact qualified
target symbol IDs to recorded domain facts. Index v3 stores the reverse postings;
v1/v2 remain readable for their existing binding and definition tools. Older
indexes report the missing impact capability explicitly rather than returning a
misleading empty result or doing an unbounded fallback scan.

The first call discovers bounded context choices. Evidence requires explicit
context selection, capped at 32 contexts. Unknown selections and simultaneous
variants of the same repository/project are rejected. Different projects can be
selected together, but every observation retains its own source snapshot and
build context. Selection does not infer a compatible dependency closure.

Each result links the requested typed contract to a recorded framework fact and
its source occurrence and optional declared owner. The matching target role is
explicit. If a registration uses the same type as service and implementation,
both roles survive without duplicating the observation. No publisher-to-consumer
delivery edge, runtime service activation, or database effect is inferred.

Queries use bounded posting work, at most 100 returned observations, cancellation,
and the existing conservative 2 MiB materialization budget. Scope filtering must
not secretly consume unbounded work. Truncation and examined-posting counts make
partial output visible. Results own their strings and use existing immutable
snapshot leases; no query reads a full audit object graph.

## Validation

Use source-authored multi-project fixtures compiled against real pinned framework
metadata. A shared contract should link producer, consumer, and storage evidence;
an identically spelled type in another project must stay separate. Capture build
variants and require exact selected-context behavior and mixed-variant rejection.
Check reverse-index completeness/corruption, genuine legacy artifacts, cancellation,
result and work budgets, source/owner provenance, and production MCP handlers.

This gate establishes recorded compiler observations, not held-out application
coverage or runtime topology. Unsupported wrappers and absent facts remain
coverage limitations rather than evidence of no impact.

## Related

- [ADR 0037: framework domain evidence](0037-compiler-framework-domain-evidence.md)
- [Domain acceptance](../../research/semantic-intelligence/DOMAIN-ACCEPTANCE.md)

- [Contract-impact acceptance](../../research/semantic-intelligence/CONTRACT-IMPACT-ACCEPTANCE.md)
