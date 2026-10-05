---
phase: 06-scoped-serving-rollout
plan: 01
type: execute
wave: 6
depends_on:
  - 05-01
files_modified:
  - internal/source/scope.go
  - internal/source/scope_test.go
  - internal/search/search.go
  - internal/search/parity_test.go
  - internal/rank/rank.go
  - internal/rank/ranker.go
  - internal/rank/ranker_test.go
  - internal/contextwin/contextwin.go
  - internal/contextwin/contextwin_test.go
  - internal/server/corpus.go
  - internal/server/rankcorpus.go
  - internal/mcp/mcp.go
  - internal/mcp/mcp_test.go
  - cmd/moedex/main.go
  - cmd/moedex-serve/main.go
  - cmd/moedex-serve/nav_lsp.go
  - cmd/moedex-serve/obs.go
  - cmd/moedex-serve/serve_test.go
  - internal/parity/branch.go
  - internal/parity/branch_test.go
  - internal/eval/gold_gate_test.go
  - internal/server/doctor.go
  - ARCHITECTURE.md
  - deploy/README.md
  - docs/plans/phases/06-scoped-serving-rollout/BENCHMARKS.md
  - docs/plans/phases/06-scoped-serving-rollout/ROLLOUT.md
autonomous: false
requirements:
  - ADR-0020
  - ADR-0021
user_setup:
  - Active required network and authenticated Git transport for final refresh/parity/capacity measurements.
  - Sibling CAS/shard/sidecar capacity and a rollback configuration retaining the default-only index.
  - Operator approval for hot-swap and soak completion.
must_haves:
  truths:
    - Zero/legacy scope returns default-branch sources only and preserves current client behavior.
    - Explicit project/branch/all scope is applied before exact limiting and ranked candidate fusion/top-K.
    - Structured and text results report an exact selected branch and commit without misleading filesystem paths.
    - Default parity/relevance do not regress, branch-scoped parity passes, and production remains one configuration switch from rollback.
    - No default or branch scope can return content excluded by that commit snapshot's privacy policy.
  artifacts:
    - path: internal/source/scope.go
      provides: Shared validated scope and deterministic eligible-source selection.
    - path: internal/search/search.go
      provides: Scope-aware exact retrieval before match emission/limiting.
    - path: internal/rank/ranker.go
      provides: Scope-aware candidate eligibility before fusion and top-K.
    - path: internal/parity/branch.go
      provides: Locked-commit ripgrep oracle and branch result identity.
    - path: internal/mcp/mcp.go
      provides: Additive scoped agent API with full source provenance.
  key_links:
    - from: internal/source/scope.go
      to: internal/search/search.go and internal/rank/ranker.go
      via: One eligibility/selection rule governs exact and ranked retrieval.
    - from: internal/rank/ranker.go
      to: internal/contextwin/contextwin.go
      via: Ranked results carry a selected eligible source instead of context choosing Files[0].
    - from: internal/contextwin/contextwin.go
      to: internal/mcp/mcp.go
      via: Context blocks retain selected project/branch/commit/path into text and structured output.
  prohibitions:
    - Never include feature-branch-only content in an unscoped request.
    - Never filter ranked results after top-K or exact results after a caller's limit.
    - Never route a non-default source to an LSP server reading the default checkout.
    - Never start or query a live LSP workspace containing effective level-1 paths unless it is a proven privacy-sanitized workspace.
    - Never cut over when a capacity, default-parity, relevance, recovery, or provenance gate is unresolved.
---

<objective>
Expose branch-aware sources through explicit scope across exact and ranked retrieval, prove parity and
relevance, then perform a reversible sibling production cutover.

Purpose: deliver the requested branch-search behavior without changing existing unscoped semantics
or presenting deduplicated content under false provenance.

Output: shared scope rules, pre-limit exact/ranked filtering, selected-source context, additive
CLI/HTTP/MCP fields, navigation guardrails, branch parity/evaluation, metrics/doctor updates, and an
operator-approved rollout report.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
@$HOME/.codex/moe/references/checkpoints.md
</execution_context>

<context>
@docs/adr/0003-cox-reduction-ripgrep-parity.md
@docs/adr/0014-eval-harness-gold-gate.md
@docs/adr/0015-structured-context-result.md
@docs/adr/0017-lsp-navigation-and-the-serena-boundary.md
@docs/adr/0020-branch-aware-indexing.md
@docs/adr/0021-ai-privacy-aware-indexing.md
@docs/plans/phases/03-branch-acquisition/PLAN.md
@docs/plans/phases/03-branch-acquisition/CAPACITY.md
@docs/plans/phases/05-source-provenance-format/PLAN.md
@docs/plans/phases/05-source-provenance-format/SUMMARY.md
@internal/search/search.go
@internal/rank/ranker.go
@internal/contextwin/contextwin.go
@internal/mcp/mcp.go
</context>

## Scope semantics

- `source.Scope{}` means default branches only across all projects.
- Project selectors intersect with every other selector and accept stable numeric ID or exact
  `path_with_namespace`; unknown/ambiguous selectors are validation errors.
- A non-empty branch list selects those exact aliases. For exact search, one source occurrence is
  emitted per requested matching branch. For ranked/context search, content stays deduplicated and
  one eligible source is selected deterministically in caller branch order, then project/snapshot/path
  order.
- `IncludeNonDefault` selects all eligible aliases; default aliases sort first. It is mutually
  exclusive with an explicit branch list to avoid ambiguous union semantics.
- Legacy shards contain only their historical default view and remain eligible under zero/default
  scope. Explicit non-default scope against a legacy-only directory returns a capability error, not
  an empty result that looks authoritative.
- Source eligibility is computed at blob-candidate entry, before postings/ranking limits. Output
  selection is separate from content identity.
- Privacy eligibility is resolved during snapshot ingestion and is never overridable by query scope;
  branch filters can narrow eligible sources but cannot restore excluded occurrences.

## Artifacts this phase produces

- `source.Scope`, `ValidateScope`, `Eligible`, and `Select` with deterministic zero-scope semantics.
- `search.LiteralScoped`/`RegexScoped`; existing functions wrap zero scope.
- `rank.RankScoped` and a selected-source field on `RankedResult`/`ContextBlock`.
- Additive CLI/HTTP/MCP request fields and structured project/branch/commit/source locators.
- Locked-commit branch parity battery, default gold regression gate, capacity/latency report, and
  production rollout/rollback report.
- `BENCHMARKS.md` for baseline-versus-actual correctness/relevance/capacity/latency evidence and
  `ROLLOUT.md` for the configuration switch, rollback, owner, and soak result.

<tasks>

<task type="auto" id="6.1">
  <name>Task 1: Implement one scope and source-selection contract</name>
  <files>internal/source/scope.go, internal/source/scope_test.go, internal/search/search.go, internal/search/parity_test.go, internal/server/corpus.go</files>
  <read_first>internal/source/ref.go, internal/search/search.go, internal/server/corpus.go, internal/search/parity_test.go</read_first>
  <action>
Implement the scope semantics above and compile each scope to per-blob eligible source selections.
Add scoped literal/regex entry points and keep current functions as zero-scope wrappers. During
exact matching, emit only eligible selected refs; an explicit multi-branch query emits branch-
identified occurrences, while zero scope emits one default occurrence per default source path. Wire
scope into corpus fan-out and sorting so global limits are applied only after all shards return
eligible matches. Include selected project ID, namespace, branch, aliases, commit, relative path,
source locator, and optional navigable path on `search.Match`. Add mixed legacy/V6 capability rules.
  </action>
  <verify>go test ./internal/source ./internal/search ./internal/server -run 'Test.*(Scope|Branch|Default|Legacy)' -count=1</verify>
  <acceptance_criteria>
    - Feature-only content is absent under zero scope and present under its explicit branch.
    - Project and branch filters intersect, and unknown/ambiguous selectors return actionable errors.
    - Scope is applied before the HTTP/CLI match limit and cannot underfill because ineligible refs consumed it.
    - Existing zero-scope literal/regex tests and exact ripgrep parity continue to pass.
  </acceptance_criteria>
  <done>Exact retrieval has one validated compatibility-preserving scope contract across shards.</done>
</task>

<task type="auto" id="6.2">
  <name>Task 2: Apply scope before ranked fusion and select context provenance</name>
  <files>internal/rank/rank.go, internal/rank/ranker.go, internal/rank/ranker_test.go, internal/contextwin/contextwin.go, internal/contextwin/contextwin_test.go, internal/server/rankcorpus.go</files>
  <read_first>internal/source/scope.go, internal/rank/rank.go, internal/rank/ranker.go, internal/contextwin/contextwin.go, internal/server/rankcorpus.go</read_first>
  <action>
Add `RankScoped` and keep `Rank` as a zero-scope wrapper. Build an eligibility bitmap before lexical,
token, dense, symbol, and path arms generate candidates; every arm and fusion/top-K must exclude
ineligible blobs. Attach the deterministic selected source to each `RankedResult`. Change context
assembly to use that selection rather than `Files[0]`, group/merge by canonical source identity plus
relative path rather than `AbsPath`, and preserve selected provenance on every block. Dense/token/
symbol content remains blob-addressed and is not duplicated per branch.
  </action>
  <verify>go test ./internal/rank ./internal/contextwin ./internal/server -run 'Test.*(Scoped|Eligible|SelectedSource|ContextBranch|TopK)' -count=1</verify>
  <acceptance_criteria>
    - An ineligible high-scoring blob cannot consume top-K or suppress a lower-scoring eligible blob.
    - All retrieval arms honor the same eligibility bitmap.
    - A branch alias requested by the caller is the branch shown by context, even when the blob's first stored ref is default or another project.
    - Two non-default sources with no absolute path never merge merely because both paths are empty.
  </acceptance_criteria>
  <done>Ranked retrieval and context selection are scoped before ranking and provenance-correct after deduplication.</done>
</task>

<task type="auto" id="6.3">
  <name>Task 3: Add scoped CLI, HTTP, MCP, and navigation contracts</name>
  <files>cmd/moedex/main.go, cmd/moedex-serve/main.go, cmd/moedex-serve/nav_lsp.go, cmd/moedex-serve/obs.go, cmd/moedex-serve/serve_test.go, internal/mcp/mcp.go, internal/mcp/mcp_test.go</files>
  <read_first>cmd/moedex/main.go, cmd/moedex-serve/main.go, cmd/moedex-serve/nav_lsp.go, internal/mcp/mcp.go, docs/adr/0015-structured-context-result.md</read_first>
  <action>
Add repeatable `-project`/`-branch` and boolean `-include-non-default` flags to applicable CLI/one-shot
paths. Add repeated `project`/`branch` and `include_non_default` query parameters to HTTP. Add the
same optional arrays/boolean to MCP `search_context` without changing required fields or zero-scope
behavior. Structured output adds `project_id`, `path_with_namespace`, `branch`, `branch_aliases`,
`commit`, `relative_path`, `source_locator` (`git:{project-id}@{commit}:{path}`), and optional
`navigable_path`; keep legacy repo/path fields where truthful. Text headers use
`path_with_namespace@branch:relative_path`. Validate request size/counts and reject an explicit
branch combined with include-all. Navigation accepts only a non-empty `NavigablePath`; structured non-default
results must not suggest calling position-based LSP tools with another checkout's path. Before a
live LSP server starts or receives a query, fail closed if the workspace policy is invalid or any
effective level-1 path is within that workspace; a future sanitized-workspace implementation may
replace this conservative block only with dedicated proof. Metrics use
bounded scope labels (`default`, `branch`, `all`), never raw project/branch names.
  </action>
  <verify>go test ./internal/mcp ./cmd/moedex-serve -run 'Test.*(Scope|Branch|Structured|Text|Navigation|Metrics)' -count=1</verify>
  <acceptance_criteria>
    - Old HTTP/MCP/CLI requests are byte/field compatible except for additive optional response fields.
    - Explicit branch requests round trip through parsing, retrieval, and structured/text rendering.
    - Non-default structured blocks omit `navigable_path`/truthful `abs_path` and identify exact commit.
    - LSP-tagged tests prove invalid/global/path-level Restricted workspaces launch no server and return no symbol/location data.
    - Raw branch/project values never become metric labels or unbounded logs.
  </acceptance_criteria>
  <done>Every supported client can request branches explicitly and receives consistent honest provenance.</done>
</task>

<task type="auto" id="6.4">
  <name>Task 4: Prove branch parity, default relevance, performance, and recovery</name>
  <files>internal/parity/branch.go, internal/parity/branch_test.go, internal/eval/gold_gate_test.go, internal/server/doctor.go, cmd/moedex-serve/obs.go, deploy/README.md, ARCHITECTURE.md, docs/plans/phases/06-scoped-serving-rollout/BENCHMARKS.md, docs/plans/phases/06-scoped-serving-rollout/ROLLOUT.md</files>
  <read_first>internal/parity/oracle_ripgrep.go, internal/parity/run.go, internal/eval/gold_gate_test.go, internal/server/doctor.go, docs/plans/phases/03-branch-acquisition/PLAN.md</read_first>
  <action>
Create a branch oracle that materializes exact locked commits only under test-owned temporary
directories and compares identity `(project, branch, commit, relative path, line, text)` with
ripgrep. Cover branch-only/modified/deleted files, alias branches, rename, force-push, shared
content, binary/BOM, symlink decision, gitlink, unusual paths, and deleted refs. Keep the existing
default full-corpus parity battery unchanged and run it first. Run the default pooled gold gate and
require no NDCG/recall/UDCG regression attributable to branch enablement. Measure actual CAS/shard
source metadata, dense chunks, peak build memory/time, refresh duration/network, daemon RSS, and
default/scoped p50/p95/p99 latency against Phase 3 thresholds. Exercise interrupted refresh, failed
fetch, failed V6 export, failed sidecar build, and daemon reload; each must retain or recover a
complete served snapshot. Update doctor/metrics/docs and `ARCHITECTURE.md` only after tests describe
the behavior that actually ships. Record redacted baseline-versus-actual evidence in
`BENCHMARKS.md` and create `ROLLOUT.md` with switch, rollback, owner, timestamps, and soak fields;
do not include tokens, credential URLs, or private project names. The branch oracle must apply the
exact commit's `.ai-privacy.yml` before ripgrep comparison and assert zero results/sidecar inputs
for every effective level-1 occurrence. Record aggregate policy and excluded-reference counts in
the rollout evidence.
  </action>
  <verify>go test ./internal/parity ./internal/eval ./internal/server -run 'Test(BranchParity|CorpusGoldGate|.*Branch.*Recovery)' -count=1</verify>
  <acceptance_criteria>
    - Default parity passes unchanged before branch-specific assertions run.
    - Every branch fixture matches the exact locked-commit ripgrep oracle with source provenance.
    - Branches with different policies at different commits expose only their own privacy-eligible result universe.
    - Default gold metrics meet their existing floors and the report records before/after values.
    - Every observed capacity/latency value is compared to an owned threshold from the approved baseline.
    - Failure injection always leaves the old or new complete directory reloadable.
  </acceptance_criteria>
  <done>Correctness, relevance, capacity, latency, and recovery have reproducible evidence before production cutover.</done>
</task>

<task type="checkpoint:human-verify" gate="blocking" id="6.5">
  <what-built>A sibling branch-aware CAS, MOEDEX06 shard directory, sidecars, and test daemon with scoped CLI/HTTP/MCP behavior.</what-built>
  <how-to-verify>Review the Phase 3 baseline-versus-actual report; run the full verification block; compare unscoped results and gold metrics to the default-only deployment; inspect feature-only, deleted, alias, force-push, and branch-specific privacy cases with exact provenance; verify non-default results have no LSP path and Restricted workspaces cannot launch/query LSP; test SIGHUP and rollback configuration; then approve the hot-swap. Keep the old directory and `ref_policy: default` configuration through the recorded soak period.</how-to-verify>
  <resume-signal>Type `approved` with the rollout/soak report path, or describe the failed gate.</resume-signal>
</task>

</tasks>

<verification>

- [ ] `make health`
- [ ] `make roundtrip`
- [ ] `make parity MOEDEX_CORPUS=/path/to/managed-corpus`
- [ ] `go test ./internal/parity -run TestBranchParity -count=1`
- [ ] `MOEDEX_CORPUS_ROOT=/path/to/managed-corpus go test ./internal/eval -run TestCorpusGoldGate -count=1`
- [ ] `make test-dense`
- [ ] `moedex-index doctor -shard-dir /path/to/branch-aware-shards`
- [ ] Unscoped, explicit-branch, all-branch, legacy-shard, reload, and rollback smoke tests pass.
- [ ] Per-snapshot privacy parity and LSP fail-closed tests pass for default and non-default branches.
- [ ] Rollout report contains baseline/actual capacity, relevance, parity, provenance samples, health, rollback, owner, and soak outcome.

</verification>

<success_criteria>

- Automated gates and the human production checkpoint pass.
- Unscoped behavior remains default-only across exact, ranked, HTTP, CLI, and MCP paths.
- Explicit branch scope returns branch-only content with exact project/branch/commit provenance.
- Query scope never widens the privacy-eligible universe and live LSP remains blocked for Restricted workspaces.
- The deployed system remains recoverable by restoring the prior configured shard directory and
  `ref_policy: default`.
- ADR 0020 can move from Proposed to Accepted/Implemented with evidence links.

</success_criteria>

<output>
After execution, create `docs/plans/phases/06-scoped-serving-rollout/SUMMARY.md` and link the final
parity, evaluation, capacity, rollout, rollback, and soak reports.
</output>
