# Program 0020: Branch-aware indexing

- **Status:** Proposed
- **ADR:** [0020](../adr/0020-branch-aware-indexing.md)
- **Depends on:** Program 0019 Phase 2
- **Goal:** Index selected remote branch tips from exact Git commits with content deduplication,
  honest project/branch/commit provenance, explicit query scope, and unchanged unscoped behavior.
- **Non-goals:** full history, tags, merge-request refs, local-only branches, one worktree per
  branch, non-default LSP navigation, or removal of legacy shard readers.

## Delivery contract

The branch-aware program is complete when:

1. a reviewed capacity baseline shows that the chosen ref policy fits the single-node envelope;
2. the managed lock records a deterministic and complete remote-head snapshot;
3. indexing reads each exact locked tree's `.ai-privacy.yml` bootstrap before any other blob and
   never requests effective level-1 content;
4. CAS refresh work is proportional to added/moved snapshots and net-new content;
5. served results retain project, branch, commit, and path provenance through persistence/reload;
6. unscoped search remains default-only while explicit branch scope returns branch-only content;
7. query scope cannot widen any snapshot's privacy-eligible universe; and
8. default and branch privacy/parity, relevance, performance, recovery, and live cutover gates pass.

## Execution phases

| Phase | Plan | Delivers | Exit gate |
|---|---|---|---|
| 3 | [Branch acquisition and exact Git-tree ingestion](./phases/03-branch-acquisition/PLAN.md) | Capacity baseline, lock v2 remote heads, NUL-safe privacy-first `ls-tree`/`cat-file` ingestion | Policy approved; zero restricted-object reads, locked-tree fixture parity, and race tests pass |
| 4 | [Branch-aware CAS and delta refresh](./phases/04-branch-aware-cas/PLAN.md) | Snapshot manifest v2 with privacy fingerprints, safe delta classification, compaction liveness, honest stats | Add/move/delete/force-push/privacy/failure tests pass without dropping prior content |
| 5 | [Source provenance and MOEDEX06](./phases/05-source-provenance-format/PLAN.md) | Explicit source identity, normalized on-disk metadata, privacy-aware freshness, legacy readers, sidecar invalidation | MOEDEX03/04/05 compatibility and MOEDEX06 round-trip/corruption/privacy tests pass |
| 6 | [Scoped retrieval, parity, and rollout](./phases/06-scoped-serving-rollout/PLAN.md) | Pre-top-K scope, additive CLI/HTTP/MCP contracts, branch privacy/parity/relevance/capacity gates, sibling cutover | Full validation and operator rollout approval pass |

## Compatibility boundary

Branch data may exist in the CAS before callers can query it, but it may not enter served shards
until Phase 5 can persist honest provenance. Branch-aware shards may be served only after Phase 6
proves that zero/legacy scope is default-only and that scope filtering happens before result limits.

## Deviation rules

- If unscoped/default results change, stop rollout; do not redefine compatibility to accept it.
- If deduplication cannot retain honest branch provenance, revise the source contract or format
  before exposing branch results.
- If the all-head policy exceeds approved capacity thresholds, change the explicit policy; never
  silently omit branches during ingestion.
- If a failed fetch or ingest can remove prior searchable content, the delta path is not shippable.
- If branch ingestion requests a level-1 object or query scope restores a privacy-excluded source,
  stop the program; privacy policy is commit-scoped and non-overridable.
