# Native CodeGraph extraction component runner

This CPU-only harness compiles byte-identical copies of the local CodeGraph
`SolutionAnalyzer`, `CodeGraphSyntaxWalker`, `GraphBuffer`, and their minimal
model/support source dependencies. It changes neither detector code nor the
reference checkout. It does **not** run the production indexing pipeline, MySQL,
REST/MCP tools, private call resolver, cross-repository linker, AI enrichment, or
embeddings. Its output supports an extraction-component comparison, not an
agent-task or production-service comparison.

The local reference has no Git metadata. `preparation.json` pins each compiled
reference source file, their canonical roster hash, the original extractor
project file, dependency versions, and adapter source files. The generated host
project uses the original six direct public package pins and a framework logging
reference. It replaces the original Services project dependency graph, whose
private TC.Common/TC.Jarvis dependencies are unavailable in the prepared cache.
The resulting `packages.lock.json`, restore/build logs and binary hash manifest
must accompany a run. Dependency warnings remain in those logs; versions are
not silently changed to improve the comparison.

Prepare and build in a **fresh** ignored persistent directory:

```sh
python3 research/semantic-intelligence/codegraph-component/runner.py prepare \
  --reference .references/CodeGraph \
  --output .local/application-impact/comparison/codegraph-build \
  --dotnet .local/application-impact/dotnet/dotnet --build
```

Freeze the independent component protocol **before extraction**, then capture:

```sh
python3 research/semantic-intelligence/codegraph-component/runner.py capture \
  --prepared .local/application-impact/comparison/codegraph-build \
  --source .local/application-impact/Sample-Outbox \
  --output .local/application-impact/comparison/codegraph-outbox \
  --dotnet .local/application-impact/dotnet/dotnet \
  --packages .local/application-impact/packages \
  --protocol research/semantic-intelligence/results/codegraph-component-outbox-20261001/protocol.json \
  --commit 1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5
```

Capture verifies a clean tracked checkout at the exact commit, creates a Git
archive projection, records every authored file hash, and supplies an explicitly
recorded empty-feed NuGet configuration. Native `SolutionAnalyzer` performs its
own restore using the supplied cached packages. The processor count is limited
to two. Source bytes are rechecked afterward. No model credentials or GPU are
required. Existing output directories are never overwritten.

Outputs preserve native schema with camelCase property names and string enum
names:

- `native/extraction.json`: raw `ExtractionResult[]`, including native nodes,
  pending edges, unresolved calls/imports, and project metadata. No added
  relationship inference or source spans.
- `native/buffer.json`: native buffer nodes and `ResolveEdges` output, plus
  unresolved calls/imports. Local numeric node IDs are assigned by sorted
  qualified name solely to supply the native resolver's required ID map.
  Raw extraction preserves duplicates that the native buffer deduplicates.
- `native/lint.json`: native analyzer's diagnostic-count cache.
- `native/execution.json`: component timing, peak working set, configuration,
  and limitations. This is not a production endpoint latency benchmark.
- `native/coverage.json`: a supplemental, separately timed MSBuildWorkspace
  probe after native extraction. It records project compilations, source hashes,
  compiler errors and workspace diagnostics. It creates no graph facts and does
  not prove that the native analyzer visited every document.
- `inputs.json`, `source-verification.json`, and capture command/stdout/stderr:
  source pin, protocol/build hashes, byte preservation, and completeness checks.

The native analyzer logs some restore/document failures and continues. Preserve
those logs; successful process exit alone is insufficient. The runner fails its
supplemental gate on missing/changed authored C# files, unavailable compilations,
compiler errors, or workspace diagnostics. Native graph nodes provide file and
line ranges; native edges do not universally provide invocation spans. Report
owner-level evidence, exact evidence, missing mappings and ambiguities separately.
