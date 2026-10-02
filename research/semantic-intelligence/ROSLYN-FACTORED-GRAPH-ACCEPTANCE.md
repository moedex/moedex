# Roslyn compact graph acceptance

Recorded September 30, 2026 (America/Denver); run manifests use October 1 UTC.
This follows the [failed expanded-storage trial](ROSLYN-GRAPH-ACCEPTANCE.md)
and implements [ADR 0035](../../docs/adr/0035-factored-graph-adjacency.md).
Publication, persisted anchors, MCP wire behavior, and unchanged refresh pass.
Run manifests, logs, native samples, and source/binary/input hashes are archived
in the [machine-readable result](results/roslyn-factored-20261001/result.json).
The subsequent [latency follow-up](ROSLYN-LATENCY-ACCEPTANCE.md) preserves the
graph and complete query responses while reducing broad-name query time and
reusing finalization work. Measurements below remain the original storage trial.

## Frozen input and publication

The source remains `dotnet/roslyn` commit
`36d26c5466e4d25940657ccb8d5b9557ccaf7be1`, using the three hash-verified shards
from snapshot `20260930T230839Z-1`. Work ran in a fresh disposable directory;
the previously published lexical/compiler snapshot was not modified.

The default pure-Go build completed with exit 0 in 355.378 seconds. The runner
sampled a peak physical footprint of 6,400,088,256 bytes (5.96 GiB), below its
8 GiB limit. It used eight Go workers, `GOMEMLIMIT=6GiB`, a 600-second deadline,
and 16 MiB per output stream. The memory guard samples the direct child every
250 ms; it is not an OS allocation cap. Two one-second native process samples
were taken during the run. These are local acceptance observations, not a
controlled speed benchmark.

| Persisted measurement | Result |
| --- | ---: |
| Graph bytes | 763,241,278 (727.88 MiB) |
| Logical edges | 182,844,081 |
| Physical records | 6,619,555 |
| Interned target sets | 69,394 |
| Nodes | 2,215,076 |
| Generation | 1 |
| Candidate confidence edges | 72,157,885 |
| Pattern confidence edges | 110,465,259 |
| Proven confidence edges | 220,937 |

The Proven edges are explicit `contains_method` relationships. Syntax call
matching has not become compiler-resolved binding. The full graph hash/build ID
is `fcc77570f2f18640264f216d29a54c68cd5253490d34270d5e0fb436efa8d002`.

Preparation took 32.662 seconds, including a 2.066-second occurrence scan.
Its pre-publication census counted 6,373,019 source records, 160,696 target sets,
253,951 target keys, and 182,597,545 logical relationships. Final persistence
interns equivalent target sets and includes additional whole-corpus passes;
these census values are intentionally distinct from the persisted totals.

## Correctness and serving gates

The independent 24-seed oracle compares full logical persisted edge and node
order against the original expanded generator/emitter, including collision,
repository, shared-content, and physical-self cases. It caught and drove a fix
for target order when the first physical self copy is excluded. Compact refresh
versus clean build, LSP reconciliation, corruption rejection, confidence upgrades,
query parity, cancellation, and explicit truncation have focused regression
coverage. Full `go test ./...`, `go vet ./...`, and the executable build passed;
targeted race and tagged LSP suites also passed. The four runner tests passed.

The persisted source gate passes both hash-pinned anchors: the real generator
`Main` has 42 Candidate sibling relationships (43 definitions including itself),
and the quoted test-program `Main` is absent as a graph node. The initial test
incorrectly used raw-file offsets. Both files have a three-byte UTF-8 BOM;
ingestion keeps their raw Git identities but strips the BOM from indexed bytes.
The corrected gate verifies raw hashes and anchor text, then translates offsets
471 to 468 and 1101 to 1098. No production code or graph changed for this fix.

The HTTP MCP gate passes schema totals, snapshot generation/build-ID/cacheability,
two `trace_calls` queries, and reverse-only `impact_analysis`. Opening the serving
snapshot took 156.518 seconds. Guarded test-process wall time was 214.233 seconds
(the test reported 213.48 seconds), and it peaked
at 3,538,013,880 sampled physical bytes. Individual observed query times were:

| Query | Seconds | Returned nodes | Returned edges |
| --- | ---: | ---: | ---: |
| `trace_calls Main`, one hop | 1.602 | 1,614 | 1,654 |
| `trace_calls Create`, one hop | 36.965 | 1,972 | 8,408 |
| `impact_analysis Create`, depth one | 17.234 | 863 | 9,148 |

All three report `truncated=true`, `total_is_exact=false`, and
`expansion_limit=10000`. The budget counts qualifying expanded relationships
before response deduplication, so returned edge counts can be smaller. Call
edges retain Pattern confidence and structurally valid evidence. This wire gate
checks evidence shape, not source-byte dereferencing; exact evidence preservation
is covered by the differential fixture oracle. These latency observations pass
the test's 90-second request deadline but are not an interactive-latency pass.

The first wire attempt passed open/schema checks but failed in the test decoder:
the public confidence tier is a string, while the internal enum only implements
JSON output. The corrected independent wire struct decodes the published shape.
Again, no production implementation or artifact changed. Both failed test attempts
are retained alongside the passing runs. Brief test compilation and anchor work
overlapped serving startup; these are not isolated performance benchmarks.

Unchanged refresh completed with exit 0 in 243.784 seconds, with sampled peak
physical footprint of 3,475,724,096 bytes. It kept generation 1 and the complete
graph SHA-256 and byte length unchanged. Logical counts and cluster status also
remained identical. Its repeated extraction/counting cost remains a performance
issue even though the artifact itself was correctly reused. Changed incremental
refresh equivalence is covered by fixture tests, not a mutation of this public
corpus during this trial.

## Clustering and remaining work

Clustering correctly publishes `over_cap`, with no partial communities:
651,659 eligible nodes exceed the 250,000-node limit, and 80,254,679 eligible
edges exceed the separate 1,000,000-edge limit. Admission counting took 598 ms.
The node limit takes precedence in the reported status. Graph persistence does
not constitute a full-corpus clustering pass.

Post-build finalization currently reopens and re-extracts the corpus for
clustering and again for counts. Reusing the live sweep and one catalog is the
next narrow performance improvement. Counts need only symbol/kind metadata;
over-cap clustering needs no location catalog. Serving startup also reconstructs
its catalog. Reverse queries scan physical source records rather than a persisted
reverse index. The optional persisted call-audit remains exhaustive. Many
overlapping repository contexts can multiply shared target-roster storage, which
this single-repository gate does not test.

A read-only query review identified repeated path-proximity computation inside
the result sort comparator and repeated root-location merging. Precomputing
proximity once per result node is a plausible ordering-preserving improvement;
CPU/allocation profiling must establish its contribution before claiming a
speedup. Startup measurements should separately time validation, extraction,
artifact hashing, catalog construction, and path indexing.

This gate does not measure compiler precision/recall, broader task quality,
private-corpus readiness, or superiority over CodeGraph. The separate selected-
project compiler/MCP gate remains [10/10 frozen cases](ROSLYN-COMPILER-ACCEPTANCE.md).
