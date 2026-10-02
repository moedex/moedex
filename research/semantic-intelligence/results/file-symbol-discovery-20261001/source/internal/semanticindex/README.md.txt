# Recorded compiler lookups

The snapshot builder derives this mapped index when given `-semantic-artifact`.
See [ADR 0029](../../docs/adr/0029-compiler-lookup-index-and-tools.md) for the format,
provenance, memory and lifetime contracts. The JSON artifact remains the audit
record; serving opens the binary index without decoding that object graph.

Start MCP with an explicit snapshot root, for example:

```sh
moedex serve -index-dir /scratch/index -mcp -embed none
```

The compiler tools remain listed even when the current snapshot lacks an index:

Prefer `compiler_symbols` after `search_context` or `file_tree` supplies an exact
`repo`/`path`. An optional case-sensitive `query` filters compiler descriptors
(e.g. `ValidateRegistration`). The response contains declaration records with
exact symbol IDs, raw hashes/offsets and separate context alternatives. It does
not require reading the file or calculating a token offset. Context choices cover
only returned matches, not every context capturing the file. Choose variants
explicitly for downstream tools; never treat alternatives as one combined build.

This lookup seeks existing position postings without rebuilding the index. It
inspects at most 10,000 occurrences in the file, including nonmatching references,
and returns at most 100 declarations within a conservative 2 MiB materialization
budget. It reports `truncated` on any bound, including an empty partial result.
There is no cursor: an absent match in a truncated response proves nothing; use
an exact binding lookup if the relevant declaration lies beyond the scan bound.
The byte bound estimates materialized records, not serialized JSON or process RSS.
`rows_visited` excludes the logarithmic seek. Missing source coverage and a file
without matching declarations both return `no_recorded_declaration`; use binding
context discovery to distinguish source coverage. No extractor or format change.

For an exact reference token or a fallback when declaration discovery truncates,
use the verified source-offset workflow below.

To discover a symbol without a supplied ID, use `list_repos` and `search_context`
to find its declaration, then `read_source` for the indexed file. Context discovery
through `compiler_binding_at` does not perform a binding query: supply offset `0`
and omit `context_id` to obtain captured contexts and their `raw_sha256` values.
Read the complete file before deriving offsets; partial/clipped text is insufficient.
Indexing strips one leading UTF-8 BOM while compiler offsets include it. Compare
SHA-256 of the complete returned UTF-8 bytes, both unchanged and with a three-byte
UTF-8 BOM prefix, with the selected context's raw hash. Only an exact match permits
converting the token position to a raw byte offset; otherwise stop and report source
mismatch. Preserve CRLF and count bytes rather than Unicode characters. No live
filesystem read or audit-artifact lookup is required for this verified join.
The [public workflow regression](../../research/semantic-intelligence/agent-journeys/check_discovery_workflow.py)
exercises two interfaces and a BOM-prefixed source in both captured context variants.

1. Call `compiler_binding_at` with `repo`, `path`, and `byte_offset` to obtain
   context choices. The offset is an exact token start in raw UTF-8 bytes, including
   any BOM. It is not a line/column pair or a normalized search offset.
2. Call again with a returned `context_id` and `raw_sha256`. The result preserves
   compiler binding status, qualified target, candidates, enclosing symbol ID,
   raw spans and generated-source provenance. Multiple facts at one token remain
   separate. An empty position does not imply an unresolved compiler binding.
3. Pass a resolved target's `symbol.id` to `compiler_definitions`. Optional `repo`
   and `context_id` filters narrow declaration variants before the result limit.
   Partial declarations can return more than one location. External symbols may
   have no captured definition.

4. Pass a domain fact target's `symbol.id` (rather than its framework API ID) to
   `compiler_contract_impact`. Omit `context_ids` to discover contexts, then select
   1–32 distinct recorded contexts for source/owner-linked framework observations.
   Variants of the same repository/project cannot be combined. Selected projects
   are not asserted to share a compatible dependency closure.

5. Use `compiler_contract_context` to combine scoped impact and definition
   evidence within a shared result/work budget.
6. Use `compiler_contract_paths` with explicit contexts for one caller step to a
   recorded direct publisher, optionally through an interface implementation.
   These are static candidate paths, not proof of dispatch or message delivery.
7. Pass an exact interface-method `symbol.id` to `compiler_implementations`.
   Omit `context_ids` to discover up to 32 contexts containing recorded
   implementations, then select contexts for declaration evidence. Each match
   includes an `implementation_fact_index` into its source binding's facts.
   Same-project context alternatives cannot be combined. Empty results describe
   missing recorded evidence, not the absence of implementations in the program.

`limit` is 1–100; truncation is explicit. Materialization has a conservative 2 MiB
per-query budget including candidate descriptions and owned result strings. An
oversized binding/definition response fails explicitly rather than dropping candidate alternatives.
`compiler_symbols` instead stops before the oversized declaration and reports truncation.
This estimate is not an exact serialized JSON or process RSS ceiling.

Version 2 adds optional `domain_facts` to bindings for recorded framework
observations. Each fact carries its rule and `compile_time` evidence scope;
targets include qualified symbol descriptors in MCP responses. These observations
do not prove runtime message delivery, service activation, or database activity.
There are at most eight facts and sixteen targets per binding. A sorted directory
locates each binding's bounded payload without scanning other domain facts, and
expanded target descriptions count toward the same query byte budget. Version 1
indexes remain readable and return no domain facts.

Version 3 adds reverse postings for exact contract targets, retaining v1/v2 reads.
`compiler_contract_impact` reports `index_upgrade_required` on older indexes.
It seeks selected contexts directly, reports examined posting counts and a
10,000-posting work ceiling, and coalesces matching roles within one observation.
Result/work bounds report truncation; materialization overflow fails explicitly.
See [ADR 0038](../../docs/adr/0038-scoped-compiler-contract-impact.md).

Version 4 adds implementation facts and method/call postings for static wrapper
paths. Binding and definition responses expose the optional facts using IDs and
a deduplicated `implementation_symbols` table. All referenced symbols count toward
materialization limits. See [ADR 0044](../../docs/adr/0044-public-compiler-implementation-evidence.md).

Version 5 adds exact interface-member reverse postings (24 bytes per relationship)
and grows the header from 384 to 400 bytes. Opening validates their complete,
unique correspondence to recorded implementation facts. Versions 1–4 remain
readable; `compiler_implementations` reports `index_upgrade_required` there.
Rebuilding the derived index from the same complete audit artifact enables the
new lookup without compiler recapture. Discovery seeks past duplicate declarations
within a context; `rows_visited` counts the representative or selected postings,
not binary-search probes. Selected queries return at most 100 relationships with
the existing 10,000-row work and 2 MiB materialization bounds. Truncation remains
explicit. See [ADR 0045](../../docs/adr/0045-reverse-compiler-implementation-discovery.md).

Responses identify the snapshot and source audit artifact, label the evidence as
`recorded-compiler-context`, and are non-cacheable. These tools query historical
captured contexts; they do not reevaluate projects, infer runtime calls, or upgrade
the syntax graph. Generated paths can be virtual and are not checkout paths.
The full captured/generated bytes and diagnostics remain in the audit artifact.

Old audit-only snapshots and explicit legacy shard directories return semantic
unavailability. A corrupt advertised index prevents a replacement from loading;
existing leased readers continue using their prior generation. A valid replacement
without semantics drops the old mapping.

Validation commands:

```sh
go test -race ./internal/semanticindex ./internal/app/servecmd ./internal/mcp
go test ./internal/semanticindex -run '^$' -fuzz '^FuzzOpen$' -fuzztime=5s -parallel=2
go test ./internal/semanticindex -run '^$' -bench . -benchtime=3x -benchmem
```

Historical pre-v3 synthetic store measurements on the development machine (Apple M5 Max): 10,000
facts occupied 3,191,518 bytes and opened in 5.32 ms with 1,656 allocated Go bytes;
100,000 facts occupied 31,901,518 bytes and opened in 56.77 ms with 1,560 allocated
Go bytes. A one-result binding lookup took 3.75 µs and allocated 1,821 bytes.
These short three-iteration measurements demonstrate the representation's heap
behavior, not production latency or pipeline capacity. Mapped pages still consume
memory; the audit import's independent 64 MiB limit still applies.
