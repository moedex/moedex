# Program 0019: Moedex-managed submodule corpus

- **Status:** Proposed
- **ADR:** [0019](../adr/0019-moedex-managed-submodule-corpus.md)
- **Goal:** A fresh authenticated operator can ask Moedex to create, snapshot, synchronize,
  validate, and index its own local corpus without adopting an existing directory.
- **Non-goals:** non-default branches, tags/MR refs, in-place adoption, a shared superproject
  remote, and implicit pruning.

## Delivery contract

The managed-corpus program is complete when:

1. `moedex-corpus init` creates a marked local superproject and curated submodules in a fresh root;
2. `.gitmodules`, gitlinks, configuration, and an atomic lock describe one committed snapshot;
3. `sync` safely handles add, update, rename, missing, explicit prune, partial fetch failure, and
   force-pushed default branches;
4. `moedex-index` consumes the managed lock without indexing the superproject or omitting
   submodules whose `.git` is a file;
5. default-only managed and conventional corpora produce equivalent exact-search results; and
6. deployment builds a sibling corpus/index and switches only after automated and human checks.

## Execution phases

| Phase | Plan | Delivers | Exit gate |
|---|---|---|---|
| 1 | [Managed corpus foundation](./phases/01-managed-corpus-foundation/PLAN.md) | Versioned marker/lock, stable project identity, hermetic fixtures, `init`, and failure-safe reconciliation | Managed lifecycle and fault-injection tests pass |
| 2 | [Managed corpus integration and rollout](./phases/02-managed-corpus-integration/PLAN.md) | CLI/doctor, lock-driven indexing, default-only parity, deployment integration, sibling cutover | `make health`, `make roundtrip`, default parity, doctor, and operator cutover approval pass |

## Dependency boundary

ADR 0020 work is blocked until Phase 2 demonstrates that a managed corpus containing only locked
default commits preserves existing search behavior. This isolates submodule/discovery regressions
from later branch/provenance changes.

## Deviation rules

- If partial submodule failure cannot carry forward the prior project snapshot, stop and revise ADR
  0019 rather than weakening the safety rule.
- If managed default-only parity differs, branch acquisition remains blocked until the difference
  is fixed or explicitly adjudicated.
- If an existing root contains any user-owned or unmanaged files, leave it untouched and use a
  sibling root.
