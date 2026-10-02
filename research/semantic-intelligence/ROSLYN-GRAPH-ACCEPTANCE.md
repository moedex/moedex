# Roslyn graph construction: preparation improved, scale gate still open

Recorded September 30, 2026 (America/Denver). This follow-up implements
[ADR 0034](../../docs/adr/0034-source-first-graph-candidates.md) against the same
public Roslyn corpus used for lexical and selected-project compiler acceptance.
It does **not** pass full graph publication acceptance.

Historical result: the subsequent compact-format implementation completed
publication on the same shards under an 8 GiB guard. See the
[compact graph acceptance record](ROSLYN-FACTORED-GRAPH-ACCEPTANCE.md).

## Inputs and outcome

The source is `dotnet/roslyn` at
`36d26c5466e4d25940657ccb8d5b9557ccaf7be1`. Three copied lexical shards came from
snapshot `20260930T230839Z-1`; their SHA-256 hashes were checked before the final
trial. The published snapshot was not modified. The graph scanner visited
31,998 shard blobs containing 440,573,723 bytes and considered 164,310 names.
Shard content totals and unique scan bytes differ because of content deduplication.

The final disposable build used eight Go workers, a 600-second deadline, and
16 MiB limits on each output stream. It completed preparation and candidate
expansion, but was stopped after 184.358 seconds when a native process sample
reported a **66.8 GB physical footprint**. This is a sample measurement, not RSS
or a continuously monitored resource maximum.

| Final trial measurement | Result |
| --- | ---: |
| One-pass occurrence scan | 3.194 s |
| Preparation, including that scan | 19.009 s |
| Candidate expansion, from second-resolution log timestamps | approximately 21 s |
| Text occurrences indexed | 9,409,508 |
| Occurrence-table accounted payload | 361,420,356 bytes |
| Source occurrences verified | 8,898,523 |
| Logical candidate pairs | 232,754,358 |
| Retained pairs before content deduplication | 182,597,653 |
| Suppressed pairs | 50,156,705 |
| Heaviest name | `Create` |
| `Create` sources × definitions | 25,559 × 852 |
| `Create` pair upper bound | 21,776,268 |

The lexical-region cache retained 440,274,445 bytes in 31,619 entries, with
1,053,636 hits, 31,619 misses, 122 coalesced waits, and no evictions. Both the
occurrence table and region cache fit their separate 512 MiB payload budgets.
Those budgets exclude other graph allocations and are not total memory limits.

Address mapping placed the sampled copy exactly at the final
`append(results[nameIndex].Edges, batch...)` in `graphrefresh.go:630` for the
recorded implementation. The builder retained all batch arrays while copying
their records into whole-name arrays. Subsequent emitter deduplication and graph
adjacency construction would need further per-edge memory. Removing that copy
alone does not solve the underlying 182.6-million-pair output.

No complete graph was persisted, so the provisioned persisted-graph anchor test
was not run. There is no full-graph correctness, query, clustering, or publication
pass in this record. The separate ten-case compiler/MCP acceptance remains valid
and separate; see [the compiler report](ROSLYN-COMPILER-ACCEPTANCE.md).

## Implemented and checked

C# extraction excludes method declarations inside normal, verbatim, and raw
strings. Verification recognizes complete raw-string delimiters, preventing
shorter embedded quote runs from exposing quoted calls as Pattern evidence.
Extractor version 3 and graph policy `scoped-bindings-v2` invalidate stale
sidecars and graph evidence on refresh.

Graph preparation now verifies sources before target expansion, shares bounded
lexical masks and compiled patterns, and builds a bounded one-pass text
occurrence table. Unsupported names and table budget overflow use the original
exact occurrence path. No target cap or ambiguity-to-resolution promotion was
introduced. Fallback posting lookup uses a single required gram and bounded
scalar count caching; C# using recognition examines the relevant line.

`go test ./...`, `go vet ./...`, and the executable build passed after the final
implementation. Focused race and differential tests cover source-first records
and suppression counters, occurrence ordering and byte boundaries, eager/lazy/
selective/content-only inputs, cache eviction and concurrent readers, malformed
raw strings, graph policy migration, repeated preparation, and cache release.
Independent reviews found no remaining blockers in these changes.

Two hash-pinned original-source anchors passed: the generator's real `Main` is
extracted, and a `Main` declaration inside a quoted test program is excluded.
This small correctness oracle is not a compiler precision/recall measurement.

## Trial history and capture correction

All run manifests and completed logs are retained in
[the result directory](results/roslyn-graph-20260930/result.json), together with
binary/input/implementation hashes, the exact capture script, and the final
native sample.

1. The first source-first trial reached its 600-second deadline. Its available
   sample showed preparation work; its logs do not establish a later phase.
2. After scalar-count and using-line changes, preparation completed in 350.393
   seconds, followed by candidate expansion. The 600-second deadline occurred
   later; this was not a preparation timeout.
3. With region and pattern caches, preparation completed in 123.621 seconds and
   expansion also finished. This trial was stopped at 468.224 seconds to include
   the newly verified raw-string correctness fix; it was not a deadline result.
4. The final one-pass trial produced the measurements above and was stopped for
   memory growth during aggregation.

The capture script buffered its log files, delaying live visibility. Earlier
live interpretations of silent files as ongoing preparation were incorrect;
the completed logs supersede those guesses. CLI logging was not suppressed.
Future capture tooling should flush each chunk and enforce a materialization
budget before allocating expanded graph output. These are single local runs
of cumulative implementations, not repeated controlled speed benchmarks.

## Next acceptance step

Preparation is no longer the principal blocker in this corpus. Persist shared
definition sets and individual source-evidence records for ambiguous names,
with exact repository filters, self-exclusions, confidence, generation, and
evidence ranges. Keep compiler-resolved facts explicit. Query expansion needs
deterministic pagination and truthful ambiguity counts, without arbitrary
winners or silently omitted targets.

First prove expanded-record parity on collision, repository, and duplicate-
content fixtures, plus incremental-versus-clean equivalence. Then rerun bounded
large-corpus publication and persisted-query gates. Streaming explicit edges
could reduce transient memory, but would retain the Cartesian product on disk
and leave query and clustering costs unresolved.
