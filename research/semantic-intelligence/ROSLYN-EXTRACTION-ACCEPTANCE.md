# Roslyn C# extraction and startup follow-up

Recorded September 30, 2026 (America/Denver); manifests use October 1 UTC.
This follows the [query-latency slice](ROSLYN-LATENCY-ACCEPTANCE.md). The target is
repeated C# regex work during symbol extraction, while preserving the accepted
extractor's behavior, graph bytes, and graph-tool responses.

## Exact extraction gate

The independent test-only oracle freezes the complete prior C# extractor,
including its declaration/reference expressions, keyword guards, C# literal
mask, raw-string handling, and body-range logic. It shares only unchanged
general scanner primitives. Its original source is archived with a checksum.

The full gate verifies the sizes and SHA-256 hashes of all three inline lexical
shards from Roslyn commit `36d26c5466e4d25940657ccb8d5b9557ccaf7be1`, snapshot
`20260930T230839Z-1`, then compares every eligible C# blob without sampling.
This authenticates indexed source bytes, raw Git identities, and file contexts.

| Exact parity measurement | Result |
| --- | ---: |
| C# blobs | 17,625 |
| Indexed C# bytes | 255,858,321 |
| Definitions | 269,945 |
| Reference occurrences | 1,472,740 |
| Guarded test-process wall time | 217.559 s |
| Sampled peak physical footprint | 647,431,320 bytes |

Definitions and references match in full ordered form: names, kinds, roles,
identifier offsets, and body ranges, including nil/empty results. Separate APIs
are additionally compared on 2,000 generated inputs and adversarial fixtures.
The corpus run overlapped some unit/race validation; its duration is a correctness
gate measurement, not an extractor speed comparison.

## End-to-end acceptance

A newly measured baseline and optimized serving run use the same accepted graph,
shards, eight-worker environment, and production MCP handlers.

| Measurement | Baseline | Optimized |
| --- | ---: | ---: |
| Serving open | 108.257 s | 35.803 s |
| Symbol loading/extraction phase | 89.930 s | 18.675 s |
| Catalog phase | 14.154 s | 13.107 s |
| `Main` call traversal | 71.7 ms | 58.9 ms |
| `Create` call traversal | 177.8 ms | 161.0 ms |
| `Create` reverse impact | 172.1 ms | 176.9 ms |
| Sampled process peak footprint | 2,515,536,272 bytes | 2,390,870,320 bytes |

All four complete structured response hashes match, including schema. Ordering,
locations, evidence, confidence, and explicit truncation remain identical.
These are single local observations, not isolated performance experiments;
the baseline overlapped some development validation. Use this pair rather than
mixing its baseline with the previous slice's 75.386-second startup observation.

The fresh full graph build completed in 104.349 seconds, with a sampled peak
physical footprint of 6,371,760,200 bytes. Its 763,241,278-byte graph has the exact
previous SHA-256 `fcc77570f2f18640264f216d29a54c68cd5253490d34270d5e0fb436efa8d002`.
All logical counts and the cluster envelope match (excluding build duration).
Both persisted source anchors pass. The earlier implementation's fresh build
was 211.003 seconds; that historical comparison is not an isolated paired trial.

Unchanged refresh completed in 18.327 seconds at 2,302,955,192 bytes sampled
peak footprint, preserving generation 1, graph bytes, cluster sidecar bytes,
and every logical count. The previous slice recorded 107.429 seconds.

The [machine-readable result](results/roslyn-extraction-20261001/result.json)
links through recorded evidence hashes to all six guarded runs, response hashes,
profiles, frozen extractor, source/binary fingerprints, and artifact comparisons.
Full unit/vet/race/tagged validation commands are also recorded in the result.
Independent review found no acceptance blocker.

## Implementation

Reference matching now uses a byte scanner with the original regex semantics:
ASCII word boundaries, optional greedy `new` with fallback, exact whitespace,
generic suffixes, and nonoverlapping matches. It advances through the opening
parenthesis before the caller applies literal and declaration-site filters.
This preserves even the original match behavior on malformed source. Matching
does not allocate a slice for every candidate that will later be discarded.

Declaration matching still uses the original expressions to choose captures.
Conservative line-head checks and bounded candidate windows avoid starting the
regex VM on impossible source regions. Multiline modifiers and generics remain
eligible. A candidate exceeding 4 KiB, excessive cumulative window scanning,
or work-counter overflow causes a whole-input fallback to the original matcher.
Raw matches drive nonoverlap even when a later mask or keyword check rejects
them. Body ranges continue to use the complete original content.

Combined definition/reference extraction now shares one immutable literal mask.
Standalone APIs retain their outputs. `ExtractorsVersion` remains 3 because this
slice changes execution, not extraction semantics; both differential tests and
the full pinned corpus support that decision. No persistent cache or freshness
shortcut is introduced.

Reference matching has 20,000 seeded differential cases plus all-byte boundary,
whitespace, malformed-generic, and `new` fallback checks. Declaration tests use
independently frozen regexes, 2,000 token-stream cases, every injected byte, and
explicit oversized/repeated-window fallback assertions. Review identified and
corrected a theoretical 32-bit work-counter overflow before corpus testing.

Full Go tests, vet, executable/test builds, focused C# race tests, and tagged LSP
checks pass on the final implementation. Independent reviews found no blocking
capture-order, mask, multiline, or complexity regression.

## Supporting microbenchmarks

Reference matching on a 27 KiB fixture improved from 3.083 ms to 55.765 microseconds,
with matching allocations dropping from 1,036 to zero. This excludes emitted
occurrence/name allocation and is not a full extraction measurement.

Declaration-only scans of the two original Roslyn anchor files improved from
282.799 ms to 135.530 ms (`SemanticErrorTests.cs`) and from 1.937 ms to 0.824 ms
(generator `Program.cs`). They exclude masks, body matching, references, and
startup I/O. Commands and transcribed tool outputs are retained separately from
end-to-end observations.

## Scope

This preserves the previous best-effort, name-based extractor, including its
limitations; it does not establish new compiler precision or recall. Large graph
queries retain their explicit 10,000-edge work budget and truthful truncation.
Clustering remains over-cap, optional persisted call auditing remains exhaustive,
and multi-repository target-roster growth is still unmeasured.

Local timing observations are not p95/p99, cold-cache, concurrency, or production
service-level measurements. The resource runner samples direct-child physical
footprint on macOS under an 8 GiB limit, eight Go workers, `GOMEMLIMIT=6GiB`, and
a 600-second deadline. It is not an OS allocation cap. CPU-profile summaries use
the previously recorded protobuf decoder because this Go installation lacks
`go tool pprof`; overlapping CPU totals are distinct from wall time.

## Subsequent serving reuse

The [persisted cache follow-up](ROSLYN-CACHE-ACCEPTANCE.md) records validated
symbol/catalog reuse, first-open costs, and corruption recovery. Measurements
above remain the historical results for the extraction slice.
