# Source discovery: pinned scale gate and compiler workflow checks

Date: 2026-10-01. Completed without subagents. This is a development resource and
public-tool regression, not independent agent scoring or a CodeGraph comparison.

## Corpus and limits

Restored `dotnet/roslyn` at commit `36d26c5466e4d25940657ccb8d5b9557ccaf7be1`
to `.local/source-discovery-scale/corpus/roslyn`. The checkout is clean and contains
35,115 tracked files; normal ingest indexes 31,998 text blobs/files. Persistent
workspace storage replaces the lost `/tmp` fixture. All three lexical shard hashes
are recorded before execution and checked afterward.

The frozen gate uses the production rank/source serving holder and HTTP MCP
handlers in a compiled Go test process, without a network listener, compiler
artifact, graph sidecar, or embeddings. Eight requests exercise repository listing,
file tree, source reading, positive and negative symbol lookup, search context,
and two explicit graph-unavailable controls. The existing external runner limits
the process to 180 seconds and an 8 GiB sampled macOS physical footprint, with
`GOMAXPROCS=2` and a 4 GiB Go memory target. Sampling is every 250 ms, not an OS
allocation cap; Go heap measurements separately force collection.

Preparation retains one interrupted command: the legacy index builder completed
shards, manifest and ranking caches, then automatically began optional graph
construction. That unnecessary phase was stopped under its guard (sampled peak
5.08 GiB). It is not reported as a successful full graph/index build. No graph was
published, and the subsequent production readers validate and use the completed
lexical artifacts.

## Measurements and correction

| Run | Elapsed process seconds | Sampled peak footprint GiB | Source opening seconds |
| --- | ---: | ---: | ---: |
| Baseline, first symbol-sidecar creation | 6.859 | 2.736 | 4.448 |
| Baseline, cached sidecars | 3.046 | 2.661 | 0.866 |
| Corrected, cached sidecars, process 1 | 3.812 | 2.707 | 0.941 |
| Corrected, cached sidecars, process 2 | 3.058 | 2.675 | 0.856 |

Every run passes the resource and source assertions. The final retained Go heap
after opening is approximately 1.77 GiB; source discovery adds about 858 MiB over
the ranker alone. This establishes a pass for this pinned corpus and these queries,
not bounded memory for arbitrary corpora, compiler artifacts, or concurrency.
These are fresh processes with separately identified sidecar cache states;
filesystem caches were not reset. No fixed latency speedup is claimed.

The baseline exposed nondeterministic `list_symbols` ordering: both runs contained
the same 31 entries but serialized them differently. Code inspection also found
that map iteration selected an arbitrary subset before the 200-result cap.
Selection now visits matching names in sorted order, and presentation breaks ties
by repository, path, line, and blob identity. Tests cover capped membership and
same-name definitions in different files. The two final processes return all eight
complete MCP responses byte-for-byte identically. Prior results remain preserved.

## Public source-to-compiler workflows

Three additional scripted checks run against the production HTTP MCP endpoint on
the unchanged Outbox v5 snapshot. They supply source-authored symbol/member names,
but obtain paths, content, context IDs, raw hashes, token offsets, and symbol IDs
only from public responses. They never read target files or compiler audit data.

| Workflow | Calls | Complete response bytes |
| --- | ---: | ---: |
| Validation interface → definitions → implementations | 11 | 45,929 |
| Submission interface → definitions → implementations | 11 | 41,413 |
| BOM-prefixed context class → bindings → definitions | 8 | 47,161 |

Both interface workflows find the recorded implementation independently in each
of two context alternatives. The third verifies the full returned source against
the context's SHA-256 with a UTF-8 BOM prefix, then uses raw offset 230 instead of
indexed offset 227. Offset conversion fails closed when neither byte representation
matches. Unit tests cover non-ASCII prefixes, CRLF, truncated/changed content,
and ambiguous anchors. Public tool descriptions and compiler lookup documentation
now explain this join.

The workflow scripts ran on the preceding source-discovery binary; the later
correction affects symbol-list ordering and tool descriptions, not their invoked
data handlers. The archive records exact binary and source hashes. These checks
are source-authored regressions, not fresh autonomous solver runs: the nine pending
independent journey tasks retain their unexecuted status.

## Validation and evidence

Full Go tests, vet, build, focused MCP/graph/serving race tests, and twelve Python
client/offset tests pass. The live test server was stopped cleanly. See the
[frozen protocol and evidence manifest](results/source-discovery-scale-20261001/result.json),
the [resource probe](../../internal/app/servecmd/source_scale_test.go), and the
[public workflow runner](agent-journeys/check_discovery_workflow.py).
