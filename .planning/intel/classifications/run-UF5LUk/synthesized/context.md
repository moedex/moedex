# Staged Context

Extracted from 2 DOC-classified documents. Content is source-attributed and verbatim
where quoted; no decisions are derived here.

## Topic: Branch-aware indexing program — delivery contract
- source: docs/plans/0020-branch-aware-indexing.md
- status (source): Proposed
- depends on (source): Program 0019 Phase 2
- goal (source): "Index selected remote branch tips from exact Git commits with content deduplication, honest project/branch/commit provenance, explicit query scope, and unchanged unscoped behavior."
- non-goals (source): "full history, tags, merge-request refs, local-only branches, one worktree per branch, non-default LSP navigation, or removal of legacy shard readers."
- delivery contract (source, verbatim clauses — acceptance-criteria-shaped but staged as context because this doc is classified DOC, not PRD):
  1. a reviewed capacity baseline shows that the chosen ref policy fits the single-node envelope;
  2. the managed lock records a deterministic and complete remote-head snapshot;
  3. indexing reads each exact locked tree's `.ai-privacy.yml` bootstrap before any other blob and never requests effective level-1 content;
  4. CAS refresh work is proportional to added/moved snapshots and net-new content;
  5. served results retain project, branch, commit, and path provenance through persistence/reload;
  6. unscoped search remains default-only while explicit branch scope returns branch-only content;
  7. query scope cannot widen any snapshot's privacy-eligible universe; and
  8. default and branch privacy/parity, relevance, performance, recovery, and live cutover gates pass.

## Topic: Branch-aware indexing program — execution phases
- source: docs/plans/0020-branch-aware-indexing.md
- Phase 3 — Branch acquisition and exact Git-tree ingestion (`./phases/03-branch-acquisition/PLAN.md`): delivers capacity baseline, lock v2 remote heads, NUL-safe privacy-first `ls-tree`/`cat-file` ingestion. Exit gate: policy approved; zero restricted-object reads, locked-tree fixture parity, and race tests pass.
- Phase 4 — Branch-aware CAS and delta refresh (`./phases/04-branch-aware-cas/PLAN.md`): delivers snapshot manifest v2 with privacy fingerprints, safe delta classification, compaction liveness, honest stats. Exit gate: add/move/delete/force-push/privacy/failure tests pass without dropping prior content.
- Phase 5 — Source provenance and MOEDEX06 (`./phases/05-source-provenance-format/PLAN.md`): delivers explicit source identity, normalized on-disk metadata, privacy-aware freshness, legacy readers, sidecar invalidation. Exit gate: MOEDEX03/04/05 compatibility and MOEDEX06 round-trip/corruption/privacy tests pass.
- Phase 6 — Scoped retrieval, parity, and rollout (`./phases/06-scoped-serving-rollout/PLAN.md`): delivers pre-top-K scope, additive CLI/HTTP/MCP contracts, branch privacy/parity/relevance/capacity gates, sibling cutover. Exit gate: full validation and operator rollout approval pass.
- note: the four phase PLAN.md files are referenced but were not part of this ingest set — they were not classified and their content is not staged here.

## Topic: Branch-aware indexing — compatibility boundary and deviation rules
- source: docs/plans/0020-branch-aware-indexing.md
- compatibility boundary (source): "Branch data may exist in the CAS before callers can query it, but it may not enter served shards until Phase 5 can persist honest provenance. Branch-aware shards may be served only after Phase 6 proves that zero/legacy scope is default-only and that scope filtering happens before result limits."
- deviation rules (source, verbatim):
  - "If unscoped/default results change, stop rollout; do not redefine compatibility to accept it."
  - "If deduplication cannot retain honest branch provenance, revise the source contract or format before exposing branch results."
  - "If the all-head policy exceeds approved capacity thresholds, change the explicit policy; never silently omit branches during ingestion."
  - "If a failed fetch or ingest can remove prior searchable content, the delta path is not shippable."
  - "If branch ingestion requests a level-1 object or query scope restores a privacy-excluded source, stop the program; privacy policy is commit-scoped and non-overridable."

## Topic: Managed-corpus program — execution phases and dependency boundary
- source: docs/plans/0019-managed-submodule-corpus.md (PRD; phase/sequencing material staged here as context, requirements staged in `requirements.md`)
- Phase 1 — Managed corpus foundation: versioned marker/lock, stable project identity, hermetic fixtures, `init`, and failure-safe reconciliation. Exit gate: Complete.
- Phase 2 — Managed corpus integration and rollout: CLI/doctor, lock-driven privacy-aware indexing, default-only parity, deployment integration, sibling cutover. Exit gate: automated gates pass; operator scope/cutover approval pending.
- dependency boundary (source): "ADR 0020 work is blocked until Phase 2 demonstrates that a managed corpus containing only locked default commits preserves existing search behavior. This isolates submodule/discovery regressions from later branch/provenance changes."

## Topic: ADR index and corpus provenance
- source: docs/adr/README.md
- content (source): "These ADRs capture the architectural decisions behind moedex, a clean-room, single-node trigram code-search engine and agent-context API (a Zoekt successor). They were consolidated from a sprint's worth of spike/latency/scale/parity working notes (2026-06); those throwaway reports have since been removed and their evidence baked into the relevant ADRs below."
- format convention (source): "lightweight ADR — Status · Context · Decision · Consequences · Evidence · Related. One decision per record."
- companion docs named (source): `ARCHITECTURE.md` (what the code is today), `docs/plans/` (implementation plans for proposed decisions), `research/` (the deep-research notes these decisions rest on), and `PARITY-REPORT.md` (the generated correctness-gate artifact, per ADR 0003).
- note: this file is an index/table-of-contents, not a decision record — it carries no decision of its own. Its per-ADR status column is a secondary restatement; where it diverges from the ADR itself, the ADR governs (see INFO in `INGEST-CONFLICTS.md`).

## Topic: Statuses as recorded by the ADR index
- source: docs/adr/README.md
- Accepted per the index: 0001, 0002, 0003, 0004, 0005, 0006, 0007, 0008, 0009, 0010, 0011, 0012, 0013, 0014, 0015, 0016, 0021, 0022.
- Proposed per the index: 0017 ("all 3 conditions met & productionized in-process: `internal/navigate`, `-tags lsp` — multi-language, pooled, incremental sync"), 0018 ("implemented: `internal/navigate` `WorkspaceSymbol`/`DocumentSymbol` + MCP tools, `-tags lsp`"), 0019 ("program and phases"), 0020 ("program and phases").
- divergence: the index lists ADR 0019 as Proposed while `docs/adr/0019-moedex-managed-submodule-corpus.md` records Status: Accepted.

## Topic: Referenced-but-unclassified sources
- source: cross-reference graph over all 25 classifications
- 22 cross-referenced targets fall outside the classified set and were not read or staged: `ARCHITECTURE.md`, `PARITY-REPORT.md`, `research/` (`fm-index-cold-tier.md`, `learned-reranker.md`, `code-embedders.md`, `symbol-layer.md`, `simd-kernel.md`), the nine `docs/plans/phases/*/PLAN.md`|`SUMMARY.md`|`ROLLOUT.md` files, and three out-of-repo paths (`../protostar`, `../moe`, `~/Code/moe/docs/adr/0001-scope-fusion-model-and-stack.md`).
- impact: any decision or acceptance criterion that lives only in those files is absent from this bundle.
