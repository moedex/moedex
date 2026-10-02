# ADR 0036: Persisted graph serving caches

Status: Accepted; local implementation and pinned Roslyn validation passed.

## Context

Opening the Roslyn graph repeats symbol extraction and reconstruction of the
node, name, and path catalogs. The extraction optimization preserves results,
but every process still pays for this work. Ranking sidecars use a different
blob-ID space and cannot serve as the graph's per-shard symbol cache.

## Decision

Keep the graph and indexed source authoritative. Add optional, rebuildable
serving caches for per-shard symbols and the graph catalog. Cache identities
include format and extractor versions and hash the actual loaded inputs with
unambiguous framing. Symbol identity preserves normalized blob ordering and
language selection. Catalog identity additionally binds the exact graph build,
blob identities, and all ordered file contexts needed for source locations.
File sizes and timestamps alone cannot establish freshness.

Read cache files defensively, checking identity, payload integrity, structural
bounds, and complete decoding before publishing any cached state. A rejected
cache follows the existing extraction/catalog path. Write complete temporary
files and atomically rename them into place; failed writes leave the successfully
built in-memory state usable. Cache availability must not become a requirement
for read-only deployments. Keep live blob/content ownership unchanged and
reconstruct pointers from the current corpus rather than persisting addresses.

Preserve ordered symbol records, reference occurrences, name lookup results,
node locations, and path entries. Cache hits must produce the same complete MCP
responses as reconstruction. These caches do not improve binding precision,
change confidence, or relax traversal and clustering limits.

## Consequences

First open pays cache construction and disk-space costs. Subsequent opens still
load source data, validate input identity and graph structure, and rebuild live
lookup containers. Neither path is a zero-copy persisted serving image.
Format or extractor changes invalidate caches. Bump the catalog format identity
when node, location, or path-index construction changes independently of the
extractor version. Corrupt, partial, stale, absent,
or unwritable caches are performance events rather than lost graph availability.

## Evidence

Acceptance requires deterministic cold/hit response parity, same-size source and
file-context invalidation, corruption and write-failure fallback, concurrent
publication, full Go validation, and guarded runs on the pinned Roslyn artifact.
Local measurements must distinguish cache creation from cache hits and compare
against a baseline collected in the same session.

## Related

- [ADR 0035: factored graph adjacency](0035-factored-graph-adjacency.md)
- [C# extraction acceptance](../../research/semantic-intelligence/ROSLYN-EXTRACTION-ACCEPTANCE.md)

- [Roslyn cache acceptance](../../research/semantic-intelligence/ROSLYN-CACHE-ACCEPTANCE.md)
