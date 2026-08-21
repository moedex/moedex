# Implementation plans

These plans translate proposed ADRs into dependency-ordered execution artifacts. The ADRs remain
authoritative for architectural decisions; each phase `PLAN.md` is the implementation and
verification contract for one delivery gate.

## Standalone remediation plans

- [Pattern call resolution and context block selection](./graph-pattern-call-resolution-and-context-selection.md)
  resolves same-name Pattern call fan-out with bounded, fail-open LSP work and replaces absolute
  header/test-file ranking gates with composable penalties.

## Program phases

| Phase | Execution plan | ADR | Wave | Depends on | Status |
|---|---|---|---:|---|---|
| 1 | [Managed corpus foundation](./phases/01-managed-corpus-foundation/PLAN.md) | [0019](../adr/0019-moedex-managed-submodule-corpus.md) | 1 | — | [Complete](./phases/01-managed-corpus-foundation/SUMMARY.md) |
| 2 | [Managed corpus integration and rollout](./phases/02-managed-corpus-integration/PLAN.md) | [0019](../adr/0019-moedex-managed-submodule-corpus.md) | 2 | Phase 1 | [Complete](./phases/02-managed-corpus-integration/SUMMARY.md) |
| 3 | [Branch acquisition and exact Git-tree ingestion](./phases/03-branch-acquisition/PLAN.md) | [0020](../adr/0020-branch-aware-indexing.md) | 3 | Phase 2 | Ready |
| 4 | [Branch-aware CAS and delta refresh](./phases/04-branch-aware-cas/PLAN.md) | [0020](../adr/0020-branch-aware-indexing.md) | 4 | Phase 3 | Proposed |
| 5 | [Source provenance and MOEDEX06](./phases/05-source-provenance-format/PLAN.md) | [0020](../adr/0020-branch-aware-indexing.md) | 5 | Phase 4 | Proposed |
| 6 | [Scoped retrieval, parity, and rollout](./phases/06-scoped-serving-rollout/PLAN.md) | [0020](../adr/0020-branch-aware-indexing.md) | 6 | Phase 5 | Proposed |

The two ADR-aligned program indexes provide the cross-phase delivery contracts:

- [Managed submodule corpus program](./0019-managed-submodule-corpus.md)
- [Branch-aware indexing program](./0020-branch-aware-indexing.md)

## Dependency spine

```text
1. versioned managed-corpus snapshot
  -> 2. default-branch indexing and operational cutover
    -> 3. locked remote heads and exact Git-tree ingestion
      -> 4. branch-aware CAS manifest and incremental refresh
        -> 5. honest persisted source provenance
          -> 6. scoped retrieval, parity, capacity gate, and cutover
```

No phase may consume a contract that is introduced by a later phase. In particular, branch fetch
does not begin until a managed default-only corpus has passed parity, and branch serving does not
begin until provenance survives a MOEDEX06 save/load round trip.

## Program invariants

Every phase must preserve these contracts:

- exact search never under-approximates ripgrep's indexed text universe;
- ambiguous discovery, network, authentication, or ingest failures carry prior content forward;
- no automatic prune follows an incomplete GitLab enumeration;
- user-owned or unmanaged files are never adopted, rewritten, or removed implicitly;
- default/unscoped search remains default-branch-only;
- the pure-Go default build remains dependency-free;
- old shard/manifests remain readable until an explicit compatibility-removal decision;
- migrations build beside the live corpus/index and switch only after validation.

## Planning status

The six phase files follow Moe's executable-plan shape: frontmatter dependencies, goal-backward
`must_haves`, explicit files, ordered tasks, `read_first`, acceptance criteria, verification, and
phase-produced artifacts. Phases 1 and 2 are complete. Phase 2's automated evidence is captured in its
[summary](./phases/02-managed-corpus-integration/SUMMARY.md) and
[rollout report](./phases/02-managed-corpus-integration/ROLLOUT.md). The managed snapshot is live
on the installed fixed executable; the production refresh exited zero and all final gates pass.
Phase 3 is ready to execute. This repository does
not currently have a `.planning/` project, so these repo-native plans are not registered in a Moe
`ROADMAP.md` or independently certified by the Moe plan-checker.
