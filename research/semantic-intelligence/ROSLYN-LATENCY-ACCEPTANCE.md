# Roslyn graph latency follow-up

Recorded September 30, 2026 (America/Denver); manifests use October 1 UTC.
This follows [compact graph acceptance](ROSLYN-FACTORED-GRAPH-ACCEPTANCE.md),
using the same pinned Roslyn commit and three verified lexical shards.
All five guarded runs passed. Logs, profiles, response hashes, source/binary
fingerprints, and run manifests are preserved in the
[machine-readable result](results/roslyn-latency-20261001/result.json).

## Query behavior and timing

The baseline and optimized test processes read the same immutable graph:
182,844,081 logical edges, 6,619,555 physical records, SHA-256/build ID
`fcc77570f2f18640264f216d29a54c68cd5253490d34270d5e0fb436efa8d002`.
Both used eight Go workers, `GOMEMLIMIT=6GiB`, CPU profiling, a 600-second process
deadline, and an 8 GiB sampled physical-footprint guard.

| Measurement | New baseline | Optimized |
| --- | ---: | ---: |
| Serving snapshot open | 83.822 s | 75.386 s |
| `trace_calls Main`, one hop | 599.696 ms | 65.842 ms |
| `trace_calls Create`, one hop | 15,384.995 ms | 161.969 ms |
| `impact_analysis Create`, depth one | 6,274.205 ms | 154.477 ms |
| Guarded process wall time | 107.178 s | 77.360 s |
| Sampled peak physical footprint | 3,503,541,896 bytes | 2,517,469,632 bytes |

All four complete structured response hashes match: schema and all three graph
queries. This checks ordered nodes/edges, locations, evidence, confidence,
truncation, and counts, not merely result cardinality. Snapshot identity and
cacheability checks also pass. All traversals continue to report truncation and
inexact totals at the unchanged 10,000 qualifying-edge work budget.

These are single sequential local acceptance runs, not a latency distribution or
controlled production benchmark. Other implementation/test work overlapped parts
of baseline startup. The earlier report's slower measurements are retained as
history; the table uses a fresh baseline from this slice. No p95/p99, cold-cache,
concurrent-client, or arbitrary-query latency claim follows.

## Changes and evidence

The fresh full build completed with exit 0 in 211.003 seconds, compared with
355.378 seconds in the preceding compact-format trial. Sampled peak physical
footprint was 6,289,479,776 bytes (5.86 GiB), below the same 8 GiB guard. The
763,241,278-byte graph has the exact same full SHA-256 as the previous build.
All logical classification/confidence/type counts match, and cluster envelope
fields match except for measured build time. The hash-pinned persisted source
anchors passed again on the fresh artifact. These separate local build trials
are acceptance observations rather than an isolated finalization benchmark.

Unchanged refresh passed in 107.429 seconds, compared with 243.784 seconds in
the preceding trial, with sampled peak physical footprint of 2,344,029,880 bytes.
The complete graph hash, generation 1, logical counts, and cluster sidecar bytes
were unchanged. Changed incremental refresh remains covered by the deterministic
fixture suite; this public checkout was not mutated for the latency run.

Result construction previously recomputed path proximity against every root
location inside the sort comparator. It now builds a directory-prefix trie and
repository set once, computes each node's proximity once, and sorts by those
unchanged ranks. Root locations are deduplicated and sorted once, retaining the
old first-wins identity and nil/empty behavior.

The baseline CPU profile attributes 17.21 sampled CPU seconds cumulatively to
the old proximity path. A paired many-root microbenchmark uses the frozen legacy
implementation and optimized implementation in one binary: 877.755 ms versus
3.587 ms, with allocations falling from 4,804,243 to 6,000. This is a one-iteration
fixture measurement, separate from the real-corpus timings above. An independent
12-seed response oracle and path edge-case tests preserve hop/confidence/name/ID
ordering, empty and absolute paths, dot components, repeated separators, Unicode,
invalid UTF-8, repository combinations, duplicate locations, and first-wins
identity collisions.

Serving startup extracts at most four shards concurrently, bounded further by
GOMAXPROCS and shard count, then publishes them in their original sorted order.
All content loads finish before workers start, and all workers finish before
publication or mapping cleanup. Mixed-language, reference, empty-shard, shared-
content, and BOM identity tests compare against serial extraction. No persistent
symbol cache or new freshness shortcut was introduced.

Catalog construction avoids singleton dedup/sort work, caches normalized path
spellings by file identity, and compares fixed-width Git IDs without constructing
full strings. Decimal offset ordering remains lexical, as in the public IDs.
Differential path-index and key-order tests cover that contract.

Build finalization now borrows the existing sweep and uses one graph/catalog for
clustering and counts. Full builds, changed refreshes, rebuild fallbacks, and
unchanged refreshes share this path; standalone tools retain independent ownership.
Tests hide shard filenames after opening a sweep to prove finalization does not
reopen/extract them. They compare exact counts and cluster membership, and exercise
borrowed ownership on success, configuration failures, missing artifacts, and
save failures. Valid generation-bound cluster caches retain their prior behavior.

Full `go test ./...`, `go vet ./...`, executable/test builds, focused race tests,
and tagged LSP checks pass. Independent reviews found no blocking ordering,
location, snapshot, ownership, or concurrency regression.

## Remaining startup and scale work

The optimized serving-open breakdown is 1.339 s validation, 64.380 s symbols,
0.634 s identity, and 9.029 s catalog, including 3.876 s path indexing. Extraction
still dominates startup. Parallel extraction is bounded but is not demonstrated
to be the cause of the modest total startup change. The optimized profile still
shows substantial C# regular-expression work; further startup work needs a
focused extraction/cache design and measurement.

Build finalization still retains sweep preparation caches until close. Incoming
queries still scan physical records; this work adds no persisted reverse index.
Clustering remains over-cap for this corpus, optional call audit remains
exhaustive, and overlapping multi-repository target-roster growth is unmeasured.
Fast bounded responses do not establish exhaustive graph answers, broader
compiler quality, or superiority over CodeGraph.

The Go installation lacks `go tool pprof`; archived CPU summaries were decoded
from the standard profile protobuf with a recorded local script. CPU sample totals
and cumulative stacks overlap and are not wall-clock phase measurements. Original
profiles are retained for analysis with a full pprof installation.

## Subsequent extraction work

The [C# extraction follow-up](ROSLYN-EXTRACTION-ACCEPTANCE.md) records the next
startup improvement and exhaustive frozen-extractor gate. Measurements above
remain the historical results for this slice.
