# ADR 0040: Compact contract context and versioned EF observations

Date: 2026-10-01
Status: Accepted

## Problem

Contract-impact tasks require both recorded framework uses and the declaration of
the contract. Separate impact and per-context definition calls repeat descriptors
and require callers to assemble evidence. The external Outbox baseline also
contains resolved EF entity operations that the initial domain rules omit.

## Decision

Add `compiler_contract_context` backed by one leased compiler index. Discovery
includes contexts with observations or declarations. Explicit selection rejects
multiple captures of the same repository/project. The response groups observations
by context and owner, shares symbol descriptors, and includes source declaration
locations. Source hashes, byte spans, extractor provenance, and exact identities
remain available. Existing impact and definition tools retain their behavior.

Reserve independent evidence and definition limits, defaulting to 80 and 20,
with a combined maximum of 100 records. Report truncation independently. Reverse
postings and a single symbol-definition scan share a 10,000-row work bound and
2 MiB materialization budget. Skipped definition rows count as work. Discovery
is bounded to 32 contexts and reports incomplete discovery explicitly. The byte
budget bounds materialized index results, not serialized transport bytes.

Worker 3 retains `csharp-framework-v1` facts and adds `csharp-framework-v2`
observations for exact metadata `DbContext.Set<TEntity>()` and
`ModelBuilder.Entity<TEntity>()` calls. The new kinds are `storage_entity_use`
and `storage_entity_mapping`. Each records one concrete entity target with
compile-time scope. Named/shared entity overloads, callback/non-generic overloads,
generic targets, unresolved calls, and local lookalikes remain excluded.

Artifact and mapped-index validation require worker 3 for v2 facts. Existing v1
facts accept workers 2 and 3. The canonical payload already carries its rule;
neither the artifact nor index format changes. Older readers fail closed on new
rules rather than silently interpreting them as old evidence.

## Consequences and validation

This reduces assembly work for a caller without inferring runtime delivery,
service activation, database activity, or compatibility between captures.
Private wrapper propagation and other framework configuration remain separate
work. Missing evidence is not proof of absence.

The original source-authored Outbox gold remains immutable. A frozen successor
promotes exactly the two EF observations. Separate frozen task labels compare
both API paths on identical published data and context choices, checking evidence,
definitions, call counts, and serialized bytes. These are internal paired tasks,
not a CodeGraph comparison or an overall application-recall score.
