# ADR 0019: Moedex-managed corpus as a local Git superproject

- **Status:** Accepted
- **Date:** 2026-08-14
- **Context owner:** moedex

## Context

`moedex-corpus` already uses an authenticated `glab` CLI to enumerate the curated set of visible,
non-archived projects on `gitlab.example.com`, then creates and refreshes independent shallow
clones under `<corpus>/<path_with_namespace>`. This proves the acquisition boundary, group
allowlist, pinned-host validation, bounded concurrency, and continue-on-error behavior. It does not,
however, make the corpus itself a versioned artifact:

- project membership is inferred by walking the filesystem;
- a successful sync has no single snapshot identity or lock recording exactly what was acquired;
- namespace renames are indistinguishable from one removal plus one addition;
- a clone, update, or prune mutates repositories independently, so an interrupted pass can leave no
  authoritative description of the mixed state;
- the current `glab auth status` preflight conflates authentication, VPN reachability, and the
  separate Git transport used by clone/fetch;
- future branch indexing needs an exact `project -> ref -> commit` handoff rather than reading refs
  while they are still being updated.

Git submodules supply the missing membership and default-checkout model, but gitlinks alone are not
enough: one gitlink records one commit, while a branch-aware corpus contains multiple branch tips.
The corpus therefore needs both a superproject and a Moedex-owned lock.

## Decision

Moedex SHALL create and maintain the corpus as a **local Git superproject** whose curated GitLab
projects are Git submodules. The superproject is an operational snapshot owned by Moedex, not a
user working tree and not, initially, a shared remote repository.

### Managed layout and ownership marker

The managed root has this shape:

```text
$MOEDEX_CORPUS/
├── .git/
├── .gitmodules
├── .moedex/
│   ├── corpus.json
│   └── corpus.lock.json
└── <path_with_namespace>/    # one Git submodule working tree per project
```

`corpus.json` is the versioned ownership/configuration marker. It records at least the schema
version, pinned GitLab host, group-selection policy, and ref-selection policy. A command MUST NOT
treat an arbitrary Git repository as a managed corpus merely because it has submodules.

`corpus.lock.json` is the authoritative acquisition-to-indexing handoff. It records each GitLab
project's stable numeric project ID, current `path_with_namespace`, validated clone URL, default
branch, exact acquired commit(s), and whether the most recent sync for that entry succeeded. The
lock is written atomically and committed with `.gitmodules` and gitlink changes after each safe
sync snapshot.

The submodule section name is derived from the stable project ID, not the namespace path. The path
remains `<path_with_namespace>`, so a GitLab rename can be reconciled as a controlled move rather
than remove-plus-add.

### Lifecycle commands

`moedex-corpus init` becomes the primary first-run command. It:

1. resolves and validates an empty/new destination;
2. runs the existing `glab`/Git preflight and enumerates the curated projects;
3. initializes the local superproject and ownership marker;
4. adds project submodules with validated, pinned-host URLs;
5. writes and commits the first lock.

The existing `clone` command remains as a compatibility alias during migration and may later be
deprecated.

`moedex-corpus sync` reconciles the complete enumerated project set against the managed catalog by
project ID. It adds new projects, fetches existing projects, moves renamed projects, updates the
default checkout/gitlink, and updates the lock. Project operations remain bounded-concurrent; the
superproject mutation and commit are serialized.

An inaccessible or no-longer-enumerated project is **reported and carried forward**. Removal still
requires explicit `--prune`, and pruning is permitted only after a complete, trustworthy GitLab
enumeration. VPN failure, authentication failure, pagination failure, or an incomplete enumeration
can never authorize removal.

When a project operation fails, its prior lock entry and gitlink are carried forward. Successful
entries may advance in the same snapshot, but the command exits non-zero and records and reports the
partial outcome. This follows the existing CAS safety rule: ambiguous external state may defer
freshness, but must not silently remove searchable content.

### Authentication and Git transport

`glab` remains the control-plane credential and project-enumeration boundary. Git clone/fetch is a
separate data-plane capability. Moedex uses the operator's configured Git protocol, validates that
every persisted URL targets the pinned host, and performs a real Git transport probe. `doctor`
reports authentication failure, host/VPN unreachability, and Git transport failure separately.
An authenticated `glab` session is necessary but is not falsely reported as sufficient when the
configured SSH or HTTPS Git transport cannot read repositories.

### Migration

Moedex SHALL NOT adopt or rewrite an existing corpus in place in the first implementation. Rollout
builds a sibling managed corpus, CAS, and shard directory; validates it; hot-swaps the daemon; and
retains the previous corpus/index through a soak period. A future explicit `adopt` command may be
designed separately, but implicit adoption is forbidden.

### AI-privacy boundary

Acquisition does not authorize indexing every acquired byte. Before any tracked content is read,
all managed and unmanaged indexing paths load the repository-root `.ai-privacy.yml`, default a
missing policy to level 3, and apply the most restrictive matching rule. Globally level-1
repositories contribute zero file references; level-1 path overrides are excluded before file
open; the policy itself is not searchable; and tracked symlinks are not followed. Invalid policies
stop publication rather than becoming ordinary skipped repositories. CAS and served manifests
record a privacy fingerprint alongside `HEAD`, so policy-only working-tree changes trigger a scrub.
The complete decision is [ADR 0021](./0021-ai-privacy-aware-indexing.md).

## Consequences

**Positive**

- Project membership, URLs, paths, and exact commits become one inspectable, versioned snapshot.
- Stable GitLab project IDs make namespace renames safe and deterministic.
- A lock provides a race-free input to indexing and the natural foundation for branch indexing
  ([0020](./0020-branch-aware-indexing.md)).
- A fresh machine can create its own corpus from `glab` access without a pre-populated mirror.
- Default-branch working trees remain available for LSP navigation and ordinary local inspection.

**Negative / costs**

- Submodule administration is more complex than independent clones: add/move/deinit/remove must
  keep `.gitmodules`, gitlinks, `.git/modules`, working trees, and the lock consistent.
- The superproject needs local commits. Moedex must use a repository-local automation identity so
  setup does not depend on the operator's global `user.name`/`user.email`.
- A local-only superproject is a snapshot and audit aid, not a durable backup; underlying project
  commits can still disappear after remote history rewrites.
- Existing discovery assumes `.git` directories, while submodule working trees normally carry a
  `.git` file. Managed indexing must consume the lock/catalog rather than index the superproject or
  miss its submodules.
- Building a sibling corpus temporarily requires enough disk for old and new corpora/indexes.

## Alternatives considered

- **Keep independent clones and add a JSON manifest.** Simpler, but leaves membership and default
  checkouts outside Git's native snapshot model and provides no gitlink-level integrity.
- **Publish one shared corpus superproject.** Rejected initially because project visibility differs
  by operator and the lock may disclose projects another user cannot access. It also creates a new
  service/ownership boundary unrelated to local search.
- **Use only bare mirrors.** Attractive for branch ingestion, but removes the checked-out default
  working trees needed by current LSP navigation and local inspection. Submodules plus fetched refs
  provide both object databases and default worktrees.
- **Adopt the existing corpus in place.** Deferred because hundreds of nested `.git` directories
  would be relocated into `.git/modules`; failure recovery and user-owned-file safety are materially
  harder than a parallel build and verified cutover.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related

[0004](./0004-content-addressable-blob-store.md),
[0010](./0010-warm-serving-spine.md),
[0011](./0011-shard-level-freshness.md),
[0017](./0017-lsp-navigation-and-the-serena-boundary.md),
[0020](./0020-branch-aware-indexing.md),
[0021](./0021-ai-privacy-aware-indexing.md);
[implementation plan](../plans/0019-managed-submodule-corpus.md).
