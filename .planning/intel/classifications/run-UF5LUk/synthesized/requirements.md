# Staged Requirements

Extracted from 1 PRD-classified document (`docs/plans/0019-managed-submodule-corpus.md`),
whose numbered "Delivery contract" clauses are the acceptance criteria. No competing
acceptance variants were found: only one PRD is present in this ingest set, so no two
requirements contend for the same scope.

Note: `docs/plans/0020-branch-aware-indexing.md` carries a structurally identical
8-clause delivery contract but was classified `DOC`, so its clauses are staged as context
rather than as requirements. See WARNINGS in `INGEST-CONFLICTS.md`.

## REQ-managed-corpus-init
- source: docs/plans/0019-managed-submodule-corpus.md
- description: `moedex-corpus init` is the primary first-run command for standing up a managed corpus in a fresh root.
- acceptance: `moedex-corpus init` creates a marked local superproject and curated submodules in a fresh root.
- scope: managed submodule corpus, moedex-corpus init, first-run bootstrap

## REQ-managed-corpus-committed-snapshot
- source: docs/plans/0019-managed-submodule-corpus.md
- description: The managed corpus state is one inspectable, atomically committed snapshot rather than loose on-disk state.
- acceptance: `.gitmodules`, gitlinks, configuration, and an atomic lock describe one committed snapshot.
- scope: .gitmodules, gitlinks, corpus configuration, corpus.lock.json, atomic commit

## REQ-managed-corpus-sync-safety
- source: docs/plans/0019-managed-submodule-corpus.md
- description: `sync` reconciles the managed corpus against GitLab without losing prior project snapshots on partial failure.
- acceptance: `sync` safely handles add, update, rename, missing, explicit prune, partial fetch failure, and force-pushed default branches.
- scope: moedex-corpus sync, submodule reconciliation, rename handling, explicit prune, partial fetch failure, force-push
- related deviation rule (source): if partial submodule failure cannot carry forward the prior project snapshot, stop and revise ADR 0019 rather than weakening the safety rule.

## REQ-managed-lock-driven-indexing
- source: docs/plans/0019-managed-submodule-corpus.md
- description: `moedex-index` consumes the managed lock as its input, correctly handling submodule working trees whose `.git` is a file rather than a directory.
- acceptance: `moedex-index` consumes the managed lock without indexing the superproject or omitting submodules whose `.git` is a file.
- scope: moedex-index, corpus.lock.json consumption, superproject exclusion, submodule `.git`-as-file

## REQ-privacy-before-content
- source: docs/plans/0019-managed-submodule-corpus.md
- description: Every indexing path — managed and conventional — applies the repository AI-privacy policy before reading content and excludes effective level-1 files.
- acceptance: every managed and conventional indexing path applies `.ai-privacy.yml` before content and excludes effective level-1 files.
- scope: .ai-privacy.yml enforcement, managed indexing path, conventional indexing path, level-1 exclusion
- related deviation rule (source): if any effective level-1 content reaches an index, CAS reference, shard, or sidecar input, stop rollout and preserve the prior live snapshot.
- upstream decision: ADR-0021 (locked)

## REQ-default-only-parity
- source: docs/plans/0019-managed-submodule-corpus.md
- description: A managed default-only corpus must not change exact-search results relative to a conventional corpus over the same privacy-eligible content.
- acceptance: default-only managed and conventional corpora produce equivalent privacy-eligible exact-search results.
- scope: default-only parity, managed vs conventional corpus, exact-search equivalence
- related deviation rule (source): if managed default-only parity differs, branch acquisition remains blocked until the difference is fixed or explicitly adjudicated.

## REQ-sibling-cutover-deployment
- source: docs/plans/0019-managed-submodule-corpus.md
- description: Deployment builds a sibling corpus/index and switches only after automated and human checks pass, never mutating an existing root in place.
- acceptance: deployment builds a sibling corpus/index and switches only after automated and human checks.
- scope: sibling cutover, deployment integration, automated gates, operator approval
- related deviation rule (source): if an existing root contains any user-owned or unmanaged files, leave it untouched and use a sibling root.
- status note (source): program Status is "In progress (Phase 2 automation complete; operator cutover approval pending)".
