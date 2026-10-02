# Roslyn persisted serving cache acceptance

Recorded October 1, 2026. This follows the
[C# extraction slice](ROSLYN-EXTRACTION-ACCEPTANCE.md) and implements
[ADR 0036](../../docs/adr/0036-persisted-graph-serving-caches.md).

## Contract

The optional caches preserve the graph's per-shard symbol identities and complete
serving catalogs. Their input digests cover normalized loaded source bytes,
blob order and identities, all ordered file contexts, and extractor versions.
The catalog also binds the exact graph build ID. They do not rely on modification
time, file length, or the ranking sidecar's different blob-ID space.

Cache misses reconstruct from authoritative graph/source inputs. Cache files
have checksummed envelopes, bounded decoding, and atomic temporary-file
publication. Failure to read or write an optional cache must not disable graph
serving. Blob pointers and content-store lifetime remain tied to the live corpus.

## Corpus and method

The frozen Roslyn commit is `36d26c5466e4d25940657ccb8d5b9557ccaf7be1`,
snapshot `20260930T230839Z-1`. A disposable copy of the accepted three shards,
graph, cluster sidecar, and manifest is used for this trial. The graph is
763,241,278 bytes with SHA-256
`fcc77570f2f18640264f216d29a54c68cd5253490d34270d5e0fb436efa8d002`.

The baseline uses the accepted extraction-slice binary on this same copy.
Candidate runs distinguish cache creation, cache hits, and corruption recovery.
Each run exercises production MCP handlers for schema, `Main` calls, `Create`
calls, and reverse `Create` impact, preserving complete structured response
hashes and snapshot identity assertions.

Local runs use the same direct-child macOS physical-footprint sampler, 8 GiB
sampled guard, eight Go workers, `GOMEMLIMIT=6GiB`, and 600-second deadline.
These are single-machine observations, not isolated trials, cold filesystem
cache tests, p95/p99 measurements, or production service-level claims.

## Measurements

| Run, in execution order | Serving open | Symbol phase | Catalog phase | Runner wall | Peak sampled footprint |
| --- | ---: | ---: | ---: | ---: | ---: |
| Uncached baseline | 16.283 s | 8.430 s | 7.069 s | 17.522 s | 2,386,512,776 bytes |
| Cache creation | 45.608 s | 14.983 s | 26.545 s | 48.800 s | 2,649,574,232 bytes |
| Cache hit | 15.141 s | 2.482 s | 9.115 s | 17.318 s | 2,108,279,424 bytes |
| Uncached baseline repeat | 41.473 s | 19.482 s | 18.810 s | 42.912 s | 2,245,724,920 bytes |
| Cache hit repeat | 12.385 s | 2.839 s | 7.895 s | 14.283 s | 2,175,388,360 bytes |
| Corruption recovery | 20.292 s | 6.790 s | 9.050 s | 21.576 s | 2,630,749,160 bytes |
| Recovered-cache hit | 12.942 s | 1.343 s | 5.914 s | 14.021 s | 2,085,833,320 bytes |

All three hit runs report all three symbol caches reused and `catalog_cached=true`.
Their files retain identical checksums and timestamps. Every run matches all
four complete structured MCP response hashes from the accepted baseline.
The graph has 2,225,451 serving catalog nodes; this differs from the graph
artifact's 2,215,076 nodes because the catalog also includes extracted symbols.

Uncached timings vary substantially, including validation and query work
unaffected by this change. Cache creation overlapped some full-unit validation;
the repeat hit briefly overlapped tagged tests. These measurements support reuse
and lower repeated work, not a stable multiplicative speedup. First-open
serialization is a real cost and is included in the cache-creation result.

The three symbol caches total 40,376,582 bytes, and the catalog is 90,152,560
bytes, for 130,529,142 bytes (124.48 MiB) of additional disposable storage.
Catalog encoding preserves ordered lists but iterates maps; equivalent rebuilt
catalog caches need not have identical byte hashes. The graph and source inputs
must remain byte-identical.

The recovery run deliberately flips the last payload byte in the catalog and
one symbol cache, leaving their headers and all authoritative inputs intact.
It reports two symbol hits, one symbol miss, and a catalog miss, then returns
all four exact baseline responses. Source shards, graph, cluster sidecar, and
manifest retain their original sizes and SHA-256 hashes. A subsequent open
reuses all regenerated caches and again returns exact baseline responses.

## Deterministic validation

Full Go tests, vet, and builds pass. Focused race checks and tagged LSP/graph
checks pass. Tests compare full MCP wire responses, including metadata, across
cold/hit opens; verify source, path, extension, repository, and SHA metadata
invalidation; exercise inline and deduplicated content; retain earlier snapshots;
and check concurrent atomic publication and failed optional writes.

Corruption tests cover every truncation boundary and flipped bytes, legacy or
malformed symbol streams, checksum-valid excessive counts and trailing data,
unknown blob IDs, out-of-source ranges, and nonregular cache paths. Decoding uses
candidate state until validation completes. Review caught untrusted count-based
preallocation in the strict symbol decoder; it now grows only as records decode.
The legacy symbol loader remains compatible with its existing formats.

Symbol cache payloads are capped at 1 GiB per shard. Catalog files are capped at
768 MiB, with limits on strings, nodes, and aggregate entries. These are format
bounds, not process-memory limits. The runner's separate sampled memory guard
measures actual corpus-run footprint.

## Evidence

The [machine-readable result](results/roslyn-cache-20261001/result.json) records
all seven guarded runs, response comparisons, artifact fingerprints, and hashes
of the archived evidence. It includes source and binary fingerprints, exact
runner code, cache file identities, and the deliberate corruption operation.
Validation command results are explicitly transcribed summaries rather than raw
logs. Independent implementation and acceptance review found no blocking issue.

## Scope

This changes reuse of existing results, not semantic precision. Clustering
remains over-cap, traversal retains explicit work limits and truncation, and
multi-repository scale and competitive quality require separate gates. Cached
open still validates and loads source data and reconstructs in-memory maps.
