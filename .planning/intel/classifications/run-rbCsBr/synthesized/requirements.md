# Staged Requirements

One PRD-classified document in this ingest set: `docs/plans/0020-branch-aware-indexing.md`
(Program 0020, Status: Proposed). Its numbered "Delivery contract" clauses are binary,
verifiable completion conditions and are extracted one requirement per clause, verbatim in
substance, with per-phase exit gates attached as acceptance where the plan pins them.

No competing acceptance variants were found: only one document in this set is classified PRD,
so no two PRDs define the same scope with divergent acceptance. Nothing was merged or deduped.

Caveat carried on every entry below: the source plan is Status **Proposed** and explicitly
defers its decision to ADR 0020, which is itself Status **Proposed**. These are candidate
requirements, not a committed contract. See INGEST-CONFLICTS.md INFO-7.

## REQ-branch-capacity-baseline
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 1)
- description: A reviewed capacity baseline must show that the chosen ref policy fits the single-node envelope before the policy is adopted.
- acceptance: a reviewed capacity baseline exists and demonstrates the chosen ref policy fits the single-node envelope. Phase 3 exit gate: policy approved.
- scope: branch ref policy; capacity baseline; single-node envelope (ADR 0001)
- status: proposed

## REQ-branch-lock-remote-heads
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 2)
- description: The managed lock records a deterministic and complete remote-head snapshot.
- acceptance: the lock's recorded remote-head snapshot is both deterministic and complete. Phase 3 delivers "lock v2 remote heads"; Phase 3 exit gate additionally requires locked-tree fixture parity and race tests to pass.
- scope: managed corpus lock; lock v2; remote heads; corpus.lock.json
- status: proposed

## REQ-branch-privacy-first-ingestion
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 3)
- description: Indexing reads each exact locked tree's `.ai-privacy.yml` bootstrap before any other blob and never requests effective level-1 content.
- acceptance: the privacy bootstrap is read before any other blob of the locked tree, and no effective level-1 content is ever requested. Phase 3 exit gate: zero restricted-object reads.
- scope: .ai-privacy.yml bootstrap; exact locked Git tree ingestion; NUL-safe ls-tree/cat-file; effective level-1 exclusion
- status: proposed
- note: this clause restates a constraint already locked by ADR 0021 (Accepted). The PRD is consistent with the higher-precedence ADR; no conflict.

## REQ-branch-proportional-cas-refresh
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 4)
- description: CAS refresh work is proportional to added/moved snapshots and net-new content.
- acceptance: refresh cost scales with added/moved snapshots and net-new content only. Phase 4 exit gate: add/move/delete/force-push/privacy/failure tests pass without dropping prior content.
- scope: CAS delta refresh; snapshot manifest v2; privacy fingerprints; delta classification; compaction liveness; honest stats
- status: proposed

## REQ-branch-provenance-persistence
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 5)
- description: Served results retain project, branch, commit, and path provenance through persistence and reload.
- acceptance: provenance survives persistence and reload intact. Phase 5 exit gate: MOEDEX03/04/05 compatibility and MOEDEX06 round-trip/corruption/privacy tests pass.
- scope: source provenance; MOEDEX06 shard format; normalized on-disk metadata; legacy shard readers; sidecar invalidation
- status: proposed

## REQ-branch-scoped-query-semantics
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 6)
- description: Unscoped search remains default-only while explicit branch scope returns branch-only content.
- acceptance: unscoped search returns default-branch content only; explicit branch scope returns branch-only content. Compatibility boundary: branch-aware shards may be served only after Phase 6 proves that zero/legacy scope is default-only and that scope filtering happens before result limits.
- scope: scoped retrieval; pre-top-K scope filtering; additive CLI/HTTP/MCP contracts; unchanged unscoped behavior
- status: proposed

## REQ-branch-scope-cannot-widen-privacy
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 7)
- description: Query scope cannot widen any snapshot's privacy-eligible universe.
- acceptance: no query scope restores a privacy-excluded source. Deviation rule: if query scope restores a privacy-excluded source, stop the program - privacy policy is commit-scoped and non-overridable.
- scope: query scope; privacy-eligible universe; commit-scoped privacy policy
- status: proposed

## REQ-branch-release-gates
- source: docs/plans/0020-branch-aware-indexing.md (Delivery contract, clause 8)
- description: Default and branch privacy/parity, relevance, performance, recovery, and live cutover gates pass.
- acceptance: all named gate families pass. Phase 6 exit gate: full validation and operator rollout approval pass.
- scope: privacy gates; parity gates; relevance gates; capacity gates; recovery; sibling cutover; operator rollout approval
- status: proposed

## Non-goals recorded by the source PRD (not requirements)
- source: docs/plans/0020-branch-aware-indexing.md (Non-goals)
- content: full history, tags, merge-request refs, local-only branches, one worktree per branch, non-default LSP navigation, and removal of legacy shard readers.

## Deviation rules recorded by the source PRD (prohibitions binding on the above)
- source: docs/plans/0020-branch-aware-indexing.md (Deviation rules)
- content: If unscoped/default results change, stop rollout; do not redefine compatibility to accept it. If deduplication cannot retain honest branch provenance, revise the source contract or format before exposing branch results. If the all-head policy exceeds approved capacity thresholds, change the explicit policy; never silently omit branches during ingestion. If a failed fetch or ingest can remove prior searchable content, the delta path is not shippable. If branch ingestion requests a level-1 object or query scope restores a privacy-excluded source, stop the program; privacy policy is commit-scoped and non-overridable.
