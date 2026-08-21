# Pattern call resolution and context block selection

- **Status:** Implemented; private candidate-graph rollout pending
- **Goal:** Replace same-name Pattern call fan-out with type-resolved targets wherever the
  production language servers can answer authoritatively, while improving context selection so
  headers and test files are penalties rather than absolute gates.
- **Non-goals:** deleting unresolved Pattern evidence, treating an empty LSP response as proof that
  a symbol is external, adding another persistent sidecar, changing the confidence floor, or
  restoring the old `>2x` enclosing-scope fallback without new evidence.

## Baseline and correction to the working estimate

The 12.5 million call-edge figure described the graph before same-repository Pattern filtering.
The current generation has 11.6 million total edges, of which roughly 2.23 million are Pattern;
the cluster view currently admits 1.19 million Pattern-or-better call/import/dependency edges.
The exact number of current Pattern call edges and, more importantly, distinct call sites has not
yet been measured. Task 1 makes that census the first deliverable so request volume is never
estimated from the old fan-out count.

The reviewed private baseline to preserve is:

| Metric | Baseline |
|---|---:|
| Recall | 0.8802 |
| Precision | 0.6414 |
| MRR | 0.7716 |
| NDCG | 0.6376 |
| Pattern precision floor/estimate | 0.0294 |
| Verified precision | 1.0000 |
| Proven precision floor/estimate | 0.4692 |
| Mean graph-tool latency | 144 ms |

The implemented read-only census now scans the persisted mmap graph directly rather than
regenerating every candidate. On the reviewed corpus it completed in 22 seconds and found
1,160,487 Pattern call edges grouped into 236,784 exact sites and 56,863 definitions.
References is the cheaper production direction: 105,459 projected requests versus 234,947
call-site Definition requests. Median fanout is 2, p95 is 16, and max is 428. These counts
justify the first rollout's References-based reconciliation while keeping Definition fallback
deferred.

Pattern precision is a conservative label-derived floor, not an estimate of all Pattern edges.
The target is therefore a measured improvement against the same reviewed labels, with no aggregate
regression, rather than an arbitrary claim about corpus-wide true precision.

## Architectural decisions

### Resolve sites, not candidate edges

Candidate generation pairs every source occurrence with every same-named definition. LSP work is
instead keyed by a call-site identity:

```text
(source blob SHA, call-site byte offset, symbol name, repository/workspace context)
```

One answer reconciles the complete target fan-out for that site. Content-deduplicated source blobs
may exist in multiple repositories, so each live workspace context is resolved separately and the
safe union is folded back into the content-addressed graph.

### Use the cheaper exact direction per workspace

The existing LSP pass performs `DocumentSymbol` for every eligible file and then `References` for
every exported definition. It adds Proven edges but leaves the sibling Pattern fan-out in place.
The replacement builds work only from Pattern call groups and chooses per workspace between:

- definition-to-references when the distinct candidate-definition count is lower; and
- call-site-to-definition when the distinct call-site count is lower or references are unsupported.

The symbol sidecar already supplies definition positions, so a full `DocumentSymbol` sweep is not
needed for job discovery. One bounded readiness probe per workspace replaces the per-file warm-up.
Requests remain sequential within one language-server route and concurrent across independent
routes.

### Exact positive answers may prune; uncertainty may not

Resolution outcomes are typed rather than collapsed into `[]Location`:

- `resolved_in_corpus`: exact returned declaration(s) map to current indexed blobs;
- `resolved_outside_corpus`: the server returned declaration locations outside the corpus;
- `ready_empty`: an initialized, warmed server returned no location;
- `unsupported`: the server does not implement the method;
- `unavailable`: spawn, cooldown, privacy, stale-file, timeout, cancellation, or server failure.

The first outcome replaces sibling Pattern targets with Proven target(s). An explicit external
location may suppress in-corpus name matches after the negative-answer fixture gate passes.
`ready_empty`, `unsupported`, and `unavailable` always retain the current Pattern edges. A null LSP
response is not proof that a framework symbol is external.

For definition-to-references reconciliation, sibling Pattern targets are removed only when every
candidate target required for that repository context completed successfully. Partial reference
coverage adds exact Proven edges but retains unresolved Pattern siblings.

For a content blob present in multiple repositories, Pattern siblings are removed only when every
applicable source context reached an authoritative result. One cold or unsupported context keeps
the conservative Pattern union.

### The graph is the persistent resolution record

No new resolution sidecar is introduced. Exact per-name edges keep `Edge.Name`, evidence offsets,
and the current graph generation. Incremental refresh carries unchanged named results from the
previous graph and resolves only dirty names/sites. Full rebuilds re-resolve from source. An
in-memory per-run cache collapses duplicate requests by route, blob SHA, byte offset, and name.

The pure-Go build remains unchanged: without `-tags lsp`, Pattern edges pass through exactly as they
do today.

### Context demotions use an internal selection score

`ContextBlock.Score` continues to expose the original fused retrieval score. Context assembly adds
an internal score used only for ordering:

```text
selectionScore = rawScore
selectionScore *= 0.4 when the block is import-dominated
selectionScore *= 0.5 when the displayed relative path is test-dominated
```

This preserves the existing useful demotion while allowing a strongly relevant import block or
test to outrank weak production code. A block with both properties receives both penalties. Raw
score then remains the first tie-break, followed by the existing deterministic keys.

Test-dominated paths are matched case-insensitively after slash normalization for the requested
set only:

- basename ending `Tests.cs`;
- basename ending `.spec.ts`;
- basename ending `_test.go`; and
- a `cypress/` path segment.

The penalty follows the displayed `Files[0]` path, preserving the current public provenance
contract for content-deduplicated blobs.

## Execution waves

### Wave 1 — Census and bounded resolver audit

#### Task 1: Add a non-mutating Pattern call census

**Likely files:** `internal/server/graphbuild.go`, `internal/server/graphcalls_lsp.go`,
`cmd/moedex-index/main.go`, colocated tests.

Add a dry-run/audit path that groups the already-filtered graph candidates before persistence and
reports, globally and by language/workspace:

- Pattern call edges and distinct call sites;
- candidate targets per site (p50/p95/max and histogram buckets);
- distinct definitions participating in those groups;
- source blobs shared across multiple repository contexts;
- sites already covered by current Proven LSP calls;
- current/exact-worktree, privacy-restricted, unsupported, and unavailable workspaces;
- projected `References` versus `Definition` request counts; and
- observed request latency/status from a deterministic, bounded sample.

The audit must not write a graph, start an unbounded corpus sweep, or alter the managed corpus. Its
JSON is the evidence used to confirm that the chosen per-workspace direction is cheaper than the
current LSP pass. A sample cap and seed make repeated runs comparable.

**Exit gate:** no corpus rollout begins until the report contains exact distinct-site counts and a
measured duration projection. The selected plan must issue no more LSP requests than the current
per-file `DocumentSymbol` plus per-definition `References` sweep for the same eligible workspaces.

#### Task 2: Expand call-specific gold coverage before changing the graph

Extend the private gold set with reviewed cases for:

- external BCL/NuGet symbols colliding with in-corpus names;
- multiple same-named methods in one repository;
- overloads, generics, extension methods, static calls, and interface dispatch;
- a successful exact target, an explicit outside-corpus target, and a warmed empty response;
- identical source content appearing in more than one repository; and
- at least C#, Go, and TypeScript where the live corpus supplies verifiable examples.

Keep existing labels and floors intact. Record Pattern call metrics separately so hierarchy-heavy
records cannot hide a call-resolution regression. Do not raise the Pattern floor until the added
labels have been reviewed and a new baseline has been recorded.

**Exit gate:** the old baseline reproduces before implementation, and every proposed suppression
case has both a positive fixture and a hard distractor.

### Wave 2 — Typed navigation and exact reconciliation

#### Task 3: Make navigation outcomes programmatically honest

**Likely files:** `internal/navigate/lsp.go`, `internal/navigate/pool.go`,
`cmd/moedex-serve/nav_lsp.go`, and tagged tests.

Introduce a detailed location-query result carrying the outcome classes above while keeping the
existing slice-returning methods as compatibility wrappers. Preserve JSON-RPC method-not-found,
successful null, and transport/server failures as distinct states. Add one bounded warm/readiness
probe per route and a bounded retry for a first empty definition response.

The MCP tool should distinguish `ready_empty`, `unsupported`, and `unavailable` in its structured
response/text instead of rendering all three as `no results`. It must not call `ready_empty`
“external”; only a returned location outside the indexed corpus earns that graph-layer label.

**Tests first:** method-not-found, cold-then-resolved, warm-empty, timeout, privacy rejection,
stale worktree, and cancellation.

#### Task 4: Reconcile Pattern groups before emission

**Likely files:** `internal/server/graphbuild.go`, `internal/server/graphrefresh.go`,
`internal/server/graphcalls_lsp.go`, a default-build stub, and colocated tests.

Refactor the LSP pass around Pattern call groups:

1. group current same-repository Pattern call candidates by source evidence and repository context;
2. create only the definition and call-site jobs participating in those groups;
3. choose the lower-request exact direction for each workspace;
4. execute through one shared pool/pacer with route-local serialization;
5. map returned locations to current indexed declaration offsets using exact file-content checks;
6. replace authoritative groups with deterministic Proven edges; and
7. retain the original Pattern group on any uncertain or partial outcome.

Exact output ordering is source identity, evidence offset, target identity. Deduplicate before
persistence. A Proven replacement retains the original call-site evidence and symbol name so dirty
name refreshes recompute it.

The first rollout enables positive reconciliation. Suppression based solely on successful
zero-reference results or explicit outside-corpus locations stays behind a separately tested option
until the Wave 1 labels demonstrate zero false suppressions.

**Tests first:** one site with many name candidates, multiple exact definitions, explicit external
location, empty/unsupported/timeout preservation, mixed content-dedup repository contexts, stale
file, route cancellation, deterministic ordering, and default-build pass-through.

#### Task 5: Make incremental refresh preserve the same contract

Add an option-aware refresh entry point used by the tagged index command. Unchanged resolved named
edges carry forward. Dirty names rebuild their Pattern groups and run only the affected workspace
jobs. Removed targets and changed target rosters invalidate the corresponding name naturally through
the existing dirty-name calculation.

Refresh must never publish a partially reconciled graph after cancellation or fatal resolver
failure. Per-site failures are non-fatal and retain Pattern edges; build-level cancellation leaves
the prior graph active.

**Tests first:** unchanged carry, dirty source re-resolution, added/removed same-name target,
unsupported-language fallback, cancellation before save, and full-build/refresh edge parity.

### Wave 3 — Context block scoring and linear-time classification

#### Task 6: Replace lexicographic import gating with composable penalties

**Likely files:** `internal/contextwin/contextwin.go`, `internal/contextwin/contextwin_test.go`.

Add `selectionScore` and the named multipliers above. Remove `importDominated` from the leading sort
tier. Keep the original `score` in every returned block.

Replace the current absolute-demotion test with a matrix proving:

- a similarly scored implementation beats an import header;
- a very high-scoring import block beats weak implementation noise;
- a production file narrowly beats a similarly scored test file;
- a strongly relevant test still beats weak production code;
- import and test penalties compose; and
- ties remain deterministic.

#### Task 7: Classify test paths and imports once per blob

Build one per-assembly blob view containing split lines, line-start offsets, and prefix counts for
nonblank/import lines. Go import-block membership is computed in the one linear scan. Every candidate
then answers the existing `>70%` test in O(1), and expansion, slicing, and clipping reuse the same
line view instead of splitting the blob again.

Add path-table tests for the four requested test conventions and near-miss production paths. Add a
deep-file benchmark/alloc test with many candidates around line 4,000 of a 5,000-line file. The
algorithmic acceptance criterion is O(total blob lines + candidates), with one line split per blob,
not a fragile wall-clock number.

#### Task 8: Keep large enclosing-scope clipping as an observed watch

Do not restore the old `>2x budget` brace/indent fallback in this change. Add regression coverage for
a very large method/class proving the salient line survives, UTF-8 and token limits remain exact,
and `Clipped` is true. Extend context evaluation output with clipped-block counts so mid-method
windows can be inspected during corpus rollout. A behavior change requires reviewed examples where
the current salient-centered clip is worse.

### Wave 4 — Corpus rollout and gates

1. Run the call census and retain its JSON report outside the repository if it contains private
   paths; commit only redacted aggregate counts.
2. Run the expanded private gold against the current graph and record the unchanged control
   baseline.
3. Build a candidate graph with the production `lsp` tag and the same dense/similarity configuration
   used by the live daemon. The private CI job must exercise this tagged resolver; a pure-Go graph
   rebuild cannot certify it.
4. Compare before/after edge counts, unique sites, authoritative resolutions, retained uncertain
   groups, targets pruned per resolved site, per-language outcomes, build duration, peak RSS, and
   graph-tool latency.
5. Run the context query set, including an implementation query, a dependency/import query, and a
   test-focused query. Record returned file classes, clipping, and token-budget conformance.
6. Publish only after every hard gate passes; then atomically swap the graph generation and rebuild
   the generation-bound cluster data.

## Hard acceptance gates

- Aggregate Recall, MRR, and NDCG remain above their committed private floors.
- Verified precision remains at least 0.9; Proven remains above its committed floor.
- Pattern call precision improves over 0.0294 on the same reviewed records; the raw before/after
  numerator and denominator are reported with the ratio.
- Every exact positive fixture returns only its LSP-resolved target set at Proven confidence.
- Empty, unsupported, failed, stale, and cancelled queries never remove Pattern edges.
- Cross-repository Pattern edges remain absent.
- The resolver request count does not exceed the old LSP pass for the same eligible workspaces, and
  measured graph-build duration does not regress beyond the audit projection.
- `search_context` always returns `token_estimate <= token_budget`.
- Dependency/import queries can still rank an import block first when its raw score warrants it.
- Test files remain retrievable for test-focused queries but lose close ranking contests to
  production files.
- Default builds and graphs remain valid without LSP binaries or the `lsp` build tag.

## Verification commands

```sh
go test ./internal/contextwin ./internal/rank -count=1
go test -tags lsp ./internal/navigate ./internal/server -count=1
go test -tags lsp -race ./internal/navigate ./internal/server -count=1
go test ./internal/eval -count=1
make health
make test-lsp
make graph-eval-private
```

The private gate uses the mounted managed shards and reviewed gold file already described by
`MOEDEX_GRAPH_EVAL_SHARDS` and `MOEDEX_GRAPH_GOLD`. The corpus rebuild command must use the same
tagged binary and ONNX configuration as production before its metrics are compared to the live
baseline.

## Stop conditions

- If the census shows the adaptive resolver still projects to an hours-long pass, stop after the
  audit and redesign the request budget; do not begin the full corpus run.
- If a language server returns unstable answers for unchanged warmed files, retain Pattern for that
  language and report it as unsupported for authoritative pruning.
- If exact reconciliation improves precision by dropping reviewed positives, stop rollout even when
  aggregate metrics remain above their floors.
- If context penalties make a reviewed dependency or test query unreachable, recalibrate the
  multiplier; do not turn either classification into a gate.
