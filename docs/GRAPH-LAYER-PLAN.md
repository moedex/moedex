# Graph Layer Plan

Replace Codegraph MCP's code-graphing with a Moedex-native graph layer — privacy-compliant, faster edge discovery, and a path to capabilities Codegraph doesn't have.

## Motivation

Codegraph MCP provides a typed knowledge graph across ~620 GitLab repos. Its graph tools (`graph_trace`, `graph_cluster`, `codegraph_search`, `graph_source`, `graph_describe_schema`) answer relationship queries that text search structurally cannot: call chains, data lineage, blast radius, pub/sub topology, service clustering.

Moedex needs to replace this capability for two reasons:
1. **Privacy compliance** — Codegraph doesn't follow our data handling policies.
2. **Architectural opportunity** — Moedex's retrieval primitives enable faster, denser, higher-confidence graph construction than Codegraph's approach.

## What Codegraph's graph actually is

Before building a replacement, the target needs demystifying. Codegraph's graph is **less magical and more sparse** than its API surface suggests.

### Data model
- **40 node types** — code (Class, Method, Interface, Route, Service, Event, Queue, Table, Column), infra (TerraformResource/Module/Variable), config (Playbook, Role, AnsibleTask).
- **34 edge types** — containment (CONTAINS_FILE/FOLDER/NAMESPACE/PROJECT), code (CALLS, IMPORTS, IMPLEMENTS, INHERITS, USES_TYPE, INJECTS), HTTP (HTTP_CALLS, HANDLES, ROUTED_TO), data flow (QUERIES, PUBLISHES, CONSUMES, SUBSCRIBES), infra (DEPLOYS, CONFIGURES, FOREIGN_KEY).

### Construction pipeline
- Built from **Roslyn-style AST parsing** of C# repos, with convention-based extractors for framework patterns: MassTransit consumer naming → `CONSUMES` edges, ASP.NET route attributes → `Route` nodes, DI registrations → `INJECTS` edges, EF queries → `QUERIES` edges.
- Also parses Ansible, Terraform, and ColdFusion. No runtime tracing, no manual annotation.

### Quality observations
- Every node carries a `trustScore` (0–1) and `doNotTrust` flag.
- **Class nodes** parsed from AST score ~0.85 with full file/line ranges.
- **Route and Event nodes** inferred from naming conventions score ~0.5 with missing file paths.
- **Call-path edges are sparse** outside pub/sub and HTTP topology — the analyzer doesn't resolve most method call chains.
- Only **267 of 769 projects** are clustered into the 32 service clusters.
- **Scheduled full rebuild**, not incremental. Cluster data shows `computedAt` timestamps refreshed daily.

## What Moedex already has

Three of the four primitives needed for graph construction exist today.

### Trigram index as cross-repo edge finder (exists)
Given a known symbol name like `OrderService.Process`, the trigram index finds every file across all shards containing that string in **sub-millisecond time**. That's a recall-complete candidate set for cross-repo edges. Codegraph does this with full AST parsing per repo — Moedex generates the same candidate list orders of magnitude faster.

### Symbol `byName` inverted index (exists)
`References("Add")` returns every blob in a shard that defines or calls that name, with byte offsets and role (definition vs reference). Within a shard, this is a **pre-built name-collision graph**. The symbol index classifies definitions as Func, Method, Type, Const, Var.

Was per-shard only; phase 1 (below) merged it into a corpus-wide lookup.

### Blob SHA content dedup (exists)
Identical content indexed once across repos. In a graph, shared/vendored library code is **one node with edges converging from multiple consumers**. Codegraph indexes per-repo with no content dedup.

### LSP type-resolved navigation (exists, spike, `-tags lsp`)
`find_references` and `find_implementations` via real language servers (gopls, csharp-ls, typescript-language-server) — actual call-graph edge discovery with type resolution. Currently per-workspace-root (one repo), not cross-repo. Expensive (one LSP request per symbol) but **higher precision than Codegraph's convention-based inference** for intra-repo edges.

## What Moedex needs to build

### Cross-shard name resolution — **delivered (phase 1)**
The `byName` inverted index was per-shard. It is now merged into a **corpus-wide name index**, so cross-repo symbol lookup works: "every repo that defines or references `AccountBillingContactChanged`."

- [`symbol.Corpus`](../internal/symbol/corpus.go) — the merge primitive. `Merge`/`AddShard` fold per-shard indices into one `byName` lookup returning `ShardRef`s (shard-qualified blob + byte range + role); `References`/`Definitions`/`DefiningShards`/`ReferencingShards` answer corpus-wide, `EachName` is the enumeration seam phase 3 fans out over. The merged map holds only name → `[shard IDs]` and resolves occurrences **on touch** from the owning shard, so it costs one entry per distinct *name*, not per occurrence.
- [`graphserve.OpenSymbols`](../internal/graph/serve/symbolcorpus.go) — builds it over a real shard dir (one symbol index per shard, merged) and resolves each hit to repo/path/line as a `SymbolSite`; `DefiningRepos`/`ReferencingRepos` are the repo-level answers. Content dedup resolves to every shard carrying the content, with the shared blob SHA as the one-content-node cue.

Not yet wired to an MCP tool — that is phase 6's surface. The serving command is untouched.

### Edge candidate generation — **delivered (phase 3)**

The recall-complete half of the two-phase construction below, in
[`internal/graph/candidates`](../internal/graph/candidates).

- `GenerateCandidates(corpus, name)` returns `Edge{Name, Source, Target, Type, Confidence, Evidence}` with
  both ends a `(shard, blob, byte offset)` `Site`. Byte offsets, not lines: a verifier
  has to be pointed at the call site, and the graph adjacency file is keyed by blob + offset.
- **Sources are the union of two arms.** The phase-1 `byName` index supplies
  extractor-classified references (precise, but only for languages with a
  RefExtractor); a **trigram fan-out over every shard** supplies every remaining byte
  occurrence, which is what reaches Python/config/SQL/comments and makes the set
  recall-complete. Where both arms find one position, the classification wins.
- **Targets are every definition of the name.** Name-based lookup cannot resolve which
  same-named definition a use points at, so all of them are candidates and phase 4
  retains unresolved pairings at lower confidence — the over-generation is the design,
  not a defect. Evidence
  strength rides along as `Type`: `SymbolReference`, `TextOccurrence`, or
  `SiblingDefinition` (a same-name definition elsewhere — an override, an
  implementation, or a plain collision, indistinguishable without type resolution).
- **The only filter is identifier delimitation** (`Add` inside `Address` is provably
  not a use of `Add`) — a necessary condition, so it cannot cost recall.
- **No silent recall loss.** A sub-trigram name, a deselected gram, or a shard
  restored without postings (the shape `server.OpenSymbols` builds) each fall back to
  a content scan instead of to the empty answer an absent posting list would look
  like; `(*Corpus).Scanning` reports which shards pay it.
- `GenerateAll(corpus, Options{})` sweeps the corpus (exported, trigram-length names
  by default) into an in-memory `Set` keyed by name — the work list for verification —
  and its `Sweep()` accounts for every name admitted and excluded, so a small result
  is never mistaken for a small corpus.

The unscored work list stays in memory as a pipeline intermediate. Phase 4 turns it
into confidence-annotated edges for phase 5; no MCP surface (phase 6) and no
`moedex serve` changes belong to candidate generation.

### Edge verification — **delivered (phase 4, regex tier)**

[`internal/graph/verify`](../internal/graph/verify) consumes the phase-3 work list
directly with `Verify(candidates)` and returns one scored edge per input edge in the
same order.

- Go patterns recognize import declarations (including package paths), direct and
  qualified calls, and common type-use shapes.
- C# patterns recognize `using` directives, calls/constructions, and common type-use
  shapes. Other languages fall back to an independently checked identifier-boundary
  match.
- A lightweight language-aware lexical mask prevents call-shaped text in comments,
  quoted strings, raw strings, and character literals from earning regex confidence.
  Go import paths are the intentional exception: their quoted location is validated as
  part of a real `import` declaration.
- Regex-confirmed edges receive the **Pattern** tier (`0.6`); everything else remains
  present as **Candidate** (`0.3`). Regex ambiguity changes confidence, never recall.

This remains name-based evidence: when one source occurrence was paired with several
same-named definitions, the regex tier can confirm the source syntax but cannot choose
the target. LSP/type-resolved verification is the later precision tier.

### Framework-aware type classifiers — **delivered (phase 2, C#)**

Moedex's symbol `Kind` was generic: Func, Method, Type, Const, Var. Codegraph's node types are architectural: Route, Service, Event, Queue, Table. That gap is now closed for **C#** — the bulk of the corpus — in [`internal/classify`](../internal/classify), with the kinds and the promotion primitive in `internal/symbol`.

- **The kinds and the seam.** `symbol.Kind` gains `Route`, `Event`, `Queue`, `Table`, `Service`, and [`(*symbol.Index).Promote(blob, nameStart, kind)`](../internal/symbol/symbol.go) is the only way to install one. It is keyed by the name's byte OFFSET rather than its name (overloads and nested declarations legitimately share a name within a blob), is idempotent, and refuses to overwrite a *different* architectural kind — so classifier ORDER is the entire precedence rule, and `ClassifyAll` deliberately runs the specific endpoint/data/messaging roles before the broad DI Service role.
- **Five C# classifiers.** ASP.NET `[Route]` / `[ApiController]` / `[HttpGet|Post|Put|Delete]` → **Route** (ApiController restricted to a type, HTTP verbs to a method, Route either); MassTransit `IConsumer<T>` plus the `*Consumer` naming convention → **Event**; EF `DbSet<T>` / `IDbContextFactory<T>` → **Table**; `IPublishEndpoint` and `IBus.Publish`, including the call sites of a typed `IBus` field → **Queue**; DI `AddScoped` / `AddTransient` / `AddSingleton<…>` → **Service**, promoting the implementation (the last type argument). When the named type's definition is not in the shard, the classifier promotes the enclosing definition instead of discarding the evidence.
- **Trigram-first, exactly as proposed below.** Each classifier turns a literal framework marker (`[Route`, `IConsumer`, `DbSet`, `AddScoped`, …) into a sound candidate blob set via `query.FromRegexp`, then runs the framework regexp — the precision gate — only over those blobs. `Report.CandidateBlobs` records the pre-confirmation fan-out, so the filter's selectivity is reported rather than assumed.
- **Two guards that carry the precision.** Confirmation is restricted to `.cs` blobs, and comments plus string / verbatim / raw literals are masked out — so `"[HttpDelete]"` in a const, a commented-out `AddSingleton<>`, or a Go file that merely mentions the markers classifies nothing. Both are covered by decoy fixtures in the tests.
- **Evidence is linked, not summarized.** A `Match` carries the evidence text with its own blob and byte offset, frequently a *different* blob from the promoted definition — DI/ORM/event markers routinely name a type declared elsewhere. That is phase 11's evidence model arriving early for this pass.
- **Prerequisite: the extractors had to stop inventing definitions.** Both regexp-based extractors were promoting statements to declarations — C# read `throw new ArgumentException(` / `return new Widget(` as a method definition, TypeScript read any indented call as a class method. `csDeclPrefixOK` (reject a prefix containing a statement keyword or ending in `new`) and `tsDeclFollowsParams` (require `{` or `:` after the parameter list) fix it: **72,255 spurious C# definitions removed** on the 491-repo corpus — 29% of every definition in it — and the `expect(` / `it(` test-framework globals gone from TS. Classifying on top of the old symbol set would have promoted noise.

Scope, honestly: **C# only**. Ansible/Terraform/ColdFusion/TypeScript framework conventions are not covered, the pass is per-shard (`*index.Index` + `*symbol.Index`, not the phase-1 corpus merge), and nothing in the build pipeline runs it yet — promotion mutates a shard's in-memory symbol index, and persisting architectural kinds is phase 5's sidecar work.

### Edge persistence & graph store — **delivered (phase 5)**

[`internal/graph/diskgraph`](../internal/graph/diskgraph) stores the verified
work list as a content-addressed adjacency sidecar:
`(blob_sha, symbol_offset) → [{edge_type, target_blob, target_offset, confidence_tier, evidence_blob, evidence_offset, evidence_length, semantic_similarity}]`.

- `Builder` is the offline write path. It interns blob SHAs once, sorts the node
  directory, and writes each node's edges contiguously as fixed-width records.
- `Open` / `Load` mmap the file. A lookup resolves the source SHA, binary-searches
  the node directory, and decodes only that adjacency range; the complete graph is
  never materialized on the Go heap.
- Confidence is persisted as the four-value tier enum; `SIMILAR_TO` additionally
  stores its exact cosine as a separate similarity metric. Byte offsets and
  lengths remain 64-bit, and the loader validates every tier, semantic score,
  evidence link, section, blob ID, sorted key, and adjacency range before serving
  a lookup.
- The offline bridge widens a call-site source to its innermost enclosing symbol
  for the adjacency key while keeping the call-site itself as a complete
  `{blob_sha, byte_offset, byte_length}` evidence link.
  Identical content repeated across shards folds to one SHA-keyed edge.
- `moedex index build`, `refresh`, and the CAS export paths now write
  `corpus-graph.graph` beside `corpus-tokens.tki` and `corpus-symbols.sym`.

### Graph query MCP tools — **delivered (phase 6)**

`moedex serve` serves `trace_calls`, `trace_consumers`, `impact_analysis`, and
`list_clusters` on both the stdio and HTTP MCP surfaces — the Codegraph equivalents of
`graph_trace` and `graph_cluster`, answered from Moedex's own graph store. All four
share one hot-swappable mmap'd generation ([`internal/graph/serve/graphtools.go`](../internal/graph/serve/graphtools.go)):
a compact symbol/location catalog for rendering, with the adjacency itself left in the
sidecar mmap and reverse traversals scanned on demand. The phase's remaining piece,
annotated `search_context`, landed with phase 14 below.

### Confidence tiers + evidence linking — **delivered (phase 11)**

Every generated and persisted edge carries one shared confidence enum and an
exact source span. **Proven** (`1.0`) is reserved for LSP-confirmed or manifest
relationships, **Verified** (`0.85`) for AST/attribute matches, **Pattern**
(`0.6`) for regex convention matches, and **Candidate** (`0.3`) for unverified
name candidates. Scores are derived from the enum and cannot drift from it.
Semantic similarity edges are Pattern-tier, so the default graph floor exposes
them, while their exact cosine remains separate in `similarity`. Cheap
name-based Pattern edges are constrained to repositories shared by source and
target; a missing same-repository definition is treated as external/unresolved,
not bound to an arbitrary corpus symbol. Verified EF query edges additionally
require a unique same-repository type/table target consistent with the source's
C# namespace/import scope. The v2 mmap sidecar interns the
evidence blob SHA and stores its 64-bit byte offset and length; graph MCP results
render confidence as `{tier, score}` and evidence as
`{blob_sha, byte_offset, byte_length}`.

### Graph-quality gate

`make health` includes a checked-in multi-language graph corpus whose gold records
invoke production MCP handlers, score their emitted node and edge order (Recall,
Precision, MRR, NDCG, and UDCG), group edge precision by confidence tier, and run a
paired `search_context` budget case for every record. `make graph-eval` runs that
hermetic tier directly. The self-hosted tier uses `make graph-eval-private` with
`MOEDEX_GRAPH_EVAL_SHARDS` and a reviewed, mounted `MOEDEX_GRAPH_GOLD` file containing
at least 30 queries, explicit coverage tags for the required graph scenarios,
reviewed hard-distractor labels, and mechanically derived floors. Duplicate
`source|type|target` edges are scored once, and same-file/same-symbol node-key
collisions receive deterministic kind/line suffixes. Floors are clamped at zero
after subtracting their fixed margins. Candidate precision and UDCG remain
logged watches; Pattern/Verified/Proven precision and aggregate Recall/MRR/NDCG
are hard gates.

### Service clustering — **delivered (phase 7)**

[`internal/graph/cluster`](../internal/graph/cluster) applies deterministic
Louvain modularity-gain moves to a weighted, undirected view of the persisted
edge graph. Parallel relationships add weight and tier scores supply edge
weight. Definitions with no eligible relationship are omitted instead of
becoming singleton communities. Stable cluster ordering and dominant
repository/namespace labels make repeated responses reproducible.

Graph build/refresh precomputes a versioned, generation-bound
`corpus-graph.clusters.json` sidecar from Pattern-or-better `calls`, `http_calls`,
`imports`, and manifest `depends_on` edges only. Pattern is necessary because
regex verification is the corpus's normal ceiling for call/import topology.
Unrelated singleton nodes are not materialized. The default eligible-node cap is 250,000
(`MOEDEX_GRAPH_CLUSTER_MAX_NODES`); an over-cap build persists counts and status but
never partial communities. A topology covering less than 1% of observed graph
nodes persists `under_covered` instead of publishing fragments as communities.

`moedex serve` exposes paginated sidecar reads as `list_clusters` on both stdio and
HTTP MCP surfaces. `{offset?,limit?}` returns summaries without members;
`{cluster_id,offset?,limit?}` returns one cluster's member page (default 50, maximum
200). Missing, stale, under-covered, and over-cap sidecars return explicit
unavailable results with observed/eligible counts and rebuild/configuration
guidance.

## Bridge architecture: trigram-accelerated graph construction

The core insight: Moedex builds edges **faster than Codegraph** using its trigram index as a cheap candidate generator, then running targeted verification only on candidates. Classic IR two-phase: recall-complete filter, then precision pass.

```
1. Extract definitions (nodes)
   Symbol index already has every definition — name, kind, byte range, blob SHA.
   Extend with framework classifiers for Route, Event, Queue, Table, Service types.
   Already indexed; classification is a post-pass over existing data.

2. Generate edge candidates (trigram fan-out)
   For each exported definition name, trigram-query the corpus.
   Sub-millisecond per query. Fleet-wide in the time Codegraph parses one repo.

3. Verify candidates (targeted precision)
   Language-specific verifier per candidate: call site, import, type reference, or string match?
   Regex patterns (cheap, ~Codegraph-level confidence) → LSP find_references (expensive, type-resolved).
   Assign the shared confidence tier per edge based on verification method.

4. Persist graph adjacency file
   Mmap'd adjacency list keyed by blob SHA + symbol offset.
   Built alongside search index — `moedex index refresh` rebuilds postings, symbols, and graph together.

5. Serve via MCP
   trace_calls, trace_consumers, impact_analysis on `moedex serve`.
   Also: annotate search_context results with graph neighborhood automatically.
```

## Phased build order

### Codegraph parity (phases 1–7)

| Phase | Delivers | Depends on | Effort |
|-------|----------|------------|--------|
| 1 | ✅ **Cross-shard symbol merge** — corpus-wide `byName` index (`symbol.Corpus`, `server.OpenSymbols`) | Existing symbol index | Medium |
| 2 | ✅ **Framework classifiers** — Route, Event, Queue, Table, Service node types via pattern matchers over trigram candidates (`internal/classify`, C# only) | Phase 1 | Medium |
| 3 | ✅ **Edge candidate generation** — trigram fan-out for each definition, cross-shard (`graph/candidates`) | Phase 1 | Low |
| 4 | ✅ **Edge verification** — language-specific call/import/type confirmation with lossless Pattern/Candidate scoring (regex tier delivered; LSP tier later) | Phase 3 | Medium–High |
| 5 | ✅ **Graph sidecar persistence** — mmap'd SHA+offset adjacency list (`graph/diskgraph`), built by `moedex index` alongside the ranking sidecars | Phase 4 | Medium |
| 6 | ✅ **Graph query MCP tools** — trace_calls, trace_consumers, impact_analysis, list_clusters, and (with phase 14) annotated search_context | Phase 5 | Medium |
| 7 | ✅ **Service clustering** — deterministic Louvain modularity optimization (`graph/cluster`) and `list_clusters` MCP output | Phase 5 | Low |

Phases 2 and 3 can run in parallel. The regex-tier verifier in phase 4 reaches Codegraph parity — Codegraph's own call edges are sparse and convention-based. LSP-tier verification is a quality improvement beyond Codegraph, not a prerequisite for replacement.

### Past Codegraph: stretch tier (phases 8–14)

These are capabilities I *assumed* Codegraph had before investigating. It doesn't. Moedex has a structural path to all of them.

| Phase | Delivers | Depends on | Effort |
|-------|----------|------------|--------|
| 8 | **Package manifest parsing** — cross-repo `DEPENDS_ON` edges from .csproj, go.mod, package.json. Proven tier, no inference. | Phases 1, 5 | Medium |
| 9 | ✅ **LSP-at-scale intra-repo call graphs** — systematic find_references for all exported symbols, prioritized by cross-repo relevance | Phases 5, 8 | High |
| 10 | **Cross-service HTTP tracing** — match client call URLs to handler route patterns across C#, Go, TypeScript, Python | Phases 2, 5 | Medium |
| 11 | ✅ **Confidence tiers + evidence linking** — replace flat trustScore with Proven/Verified/Pattern/Candidate tiers, each carrying blob SHA + byte offset + byte length evidence | Phase 5 | Low |
| 12 | **Incremental graph refresh** — CAS-aware delta recompute for changed blobs only, git-based edge timestamps | Phase 5 | Medium |
| 13 | ✅ **Semantic similarity edges** — ONNX embedder `SIMILAR_TO` edge type for near-duplicate and pattern-match discovery across repos | Phases 5, dense build | Medium |
| 14 | ✅ **Graph-fused search** — `search_context` results annotated with graph neighborhood automatically, no second tool call | Phases 5, 6 | Low |

Phase 13 is implemented behind the existing `onnx` tag. The tagged indexer
embeds one whole-definition chunk per content-addressed symbol, performs an
exact corpus-wide cosine top-K pass, and persists directed `SIMILAR_TO` edges
at Pattern confidence with exact cosine stored separately; the definition body
is their evidence span. Top-K and threshold are configurable with
`MOEDEX_GRAPH_SIMILAR_TOP_K` and `MOEDEX_GRAPH_SIMILAR_THRESHOLD`. Graphs with
at most 4,096 definitions use exact all-pairs cosine. Larger graphs use
deterministic angular-LSH candidate generation followed by exact cosine
reranking, capped at 2,048 candidates per definition. This bounds the
large-corpus pass linearly while retaining exact persisted scores and stable
ordering. `MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT` and
`MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES` tune those bounds; build logs expose the
mode, indexed definitions, exact comparison count, edge count, and elapsed time.

Phase 9 is implemented behind the existing `lsp` tag. During graph
construction it enumerates indexed source files with LSP `documentSymbol`, then
issues `find_references` for every exported symbol. Symbols whose Phase 3 work
list contains a cross-shard edge are queried first. Work is grouped by the exact
`(language, workspace)` server route: requests to one server stay sequential while
independent routes run through a bounded pool (default `min(2*GOMAXPROCS, 32)`).
Aggregate starts are capped at 100/second and every request remains individually
timeout-bound. `MOEDEX_GRAPH_LSP_CONCURRENCY` and
`MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND` tune those bounds. Workspace privacy is
validated before any language server launches. Only
intra-repo references with call syntax and an enclosing caller become `CALLS`
edges. They persist at Proven (`1.0`) with the exact call-site evidence span and
upgrade a matching regex edge in place. Short exported names and nested
interface methods therefore remain covered even when the trigram-length regex
sweep cannot generate them.

**Phase 8** (package manifests) is the highest-value stretch item — proven cross-repo edges with zero inference.

### Graph-fused search — **delivered (phase 14)**

`search_context` now answers with the graph attached. Every ranked context block
carries a `neighbors` field, so an agent that found a symbol never needs a second
tool call to learn what calls it, what it depends on, or who publishes the event it
handles — the endgame of having search and graph in the same engine.

- **The seam.** [`mcp.GraphAnnotator`](../internal/mcp/neighbors.go) is the contract;
  `GraphToolset` implements it in
  [`internal/graph/serve/graphneighbors.go`](../internal/graph/serve/graphneighbors.go), and
  `moedex serve` wires the two with `mcp.WithGraphAnnotator` on both the stdio and
  HTTP MCP surfaces. The `mcp` package owns the contract and the rendering only — the
  mmap'd graph never leaves `internal/graph/serve`.
- **`graph_depth`** (default 1, `0` disables, max 10) is the per-call control. It is a
  *pointer* in the argument struct, so an omitted argument and an explicit `0` are
  distinguishable rather than colliding on Go's zero value.
- **`min_confidence`** defaults to Pattern and is applied before traversal on
  `search_context` and every standalone graph traversal. Candidate edges remain
  available only when explicitly requested and cannot bridge a default traversal.
- **The join is positional.** A context block knows a path and a 1-based line range;
  the node catalog knows where every graph node lives. A path→nodes index built once
  per graph generation resolves one to the other, trying the absolute path first, then
  repo-qualified, then bare — so a bare relative path can never pull in a same-named
  file from another repo once the absolute path has identified the real one.
- **Six directed lanes.** `callers` (incoming CALLS), `callees` (outgoing CALLS),
  `consumers` (incoming CONSUMES), `publishers` (incoming PUBLISHES), `depends_on`
  (outgoing IMPORTS/USES_TYPE/REFERENCES/CANDIDATE/PUBLISHES/CONSUMES), `similar_to`
  (either direction). Each traverses in one fixed direction, which is what makes
  `graph_depth > 1` mean "callers of callers" rather than an undirected blob. A
  manifest-derived `DEPENDS_ON` type (phase 8) joins the `depends_on` lane when it
  lands.
- **Stated scope, not silent scope.** Incoming reference/import edges are deliberately
  not annotated — the highest-volume edge class in the graph, and `impact_analysis`
  already owns that question. Buckets retain true pre-cap `bucket_totals`; the
  compatibility `truncated` flag is derived from those totals and text reports
  `+N more (of M total)`. A block whose symbols have no edges gets a **present, empty**
  annotation, so "graph off" and "nothing found" never look alike.
- **One sweep per hop, not one per block.** The sidecar stores forward adjacency only,
  so reverse lanes have to scan. `diskgraph.EachEdge` (new) makes that scan a linear,
  allocation-free pass over the node directory and edge section, and every incoming
  lane of every block shares a single sweep per hop. It replaced the
  `Keys()`+`Edges()` shape inside `graphSnapshot.incoming`, which cost a binary search
  and a slice allocation per node — acceptable for an explicit `trace_calls`, not for
  something search now does by default. The existing graph tools get the speedup too.

## Out of scope

These are Codegraph integrations that share its MCP server but are not code-graphing. They're separate problem domains.

| Domain | What it is | Why it's not Moedex's problem |
|--------|-----------|-------------------------------|
| Observability | Grafana dashboards, alerts, incidents, log queries | Runtime monitoring, not code structure |
| Infrastructure | Consul KV, RabbitMQ peek, MySQL schema/queries | Live infra state, not code relationships |
| Memory | Per-user claim graph, RAG over operating docs | Institutional knowledge layer, not code analysis |
| Project mgmt | Full Shortcut API (stories, epics, workflows) | Work-tracking SaaS integration |
| SCM | GitLab commits, MRs, CI, file-at-ref | API wrappers, not graph analysis (borderline) |

## Key design principles

- **The default build stays pure Go.** Graph construction and serving use only the standard library. LSP-tier verification is behind the existing `lsp` build tag. Semantic similarity edges are behind the existing `onnx` build tag.
- **Trigram-first, verify-second.** Every edge discovery starts with a cheap trigram candidate query, then applies the cheapest verifier that reaches the desired confidence tier.
- **Mmap'd like postings.** The graph adjacency file follows the existing `diskstore` pattern — built offline, served via mmap, never enters the Go heap.
- **Rebuild with the index.** `moedex index refresh` rebuilds postings, symbols, tokens, and graph together. No separate graph build pipeline.
- **Bounded hot-name scheduling.** Candidate sources are prepared once per name,
  then high-frequency names are split into deterministic source batches of about
  32K candidate pairs. The shared largest-first queue reports its heaviest name
  before work and active batches every 30 seconds, avoiding the old one-core tail.
