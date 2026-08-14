# ADR 0020: Branch-aware indexing from locked Git trees

- **Status:** Proposed
- **Date:** 2026-08-14
- **Context owner:** moedex (TurnCommerce)

## Context

Moedex currently indexes one checked-out Git state per repository. `ingest.Repo` reads
`git ls-files -s`, opens files from the working tree, and freshness compares `git rev-parse HEAD`
against one recorded commit. The CAS manifest likewise records one ordered file set per repository,
and served `FileRef` provenance contains only repository, relative path, and absolute path.

Indexing remote branches is not a matter of running the existing ingestion loop several times:

- one working tree can represent only one branch at a time;
- materializing one worktree per branch multiplies disk usage and introduces checkout races;
- the same content appears across many branches, so storage must retain provenance without
  re-indexing or re-embedding identical blobs;
- a branch can move, be force-pushed, be renamed, or disappear independently of the default branch;
- a non-default branch file has no honest absolute path in the default checkout;
- unqualified queries over every branch would duplicate results and change current relevance;
- the ranked path currently reports the first `FileRef` on a deduplicated blob, which cannot select
  correct branch provenance or apply a branch filter before top-K.

ADR [0019](./0019-moedex-managed-submodule-corpus.md) supplies the missing stable input: an atomic
corpus lock mapping project IDs and remote branch names to exact commits.

## Decision

Moedex SHALL index selected remote branch tips by reading the **exact Git trees named by the managed
corpus lock**, without checking out non-default branches.

### Indexed ref set and snapshot identity

The initial branch policy is the depth-one tip of every selected `refs/heads/*` visible through the
configured `origin`; full branch history is not acquired or indexed.
Tags, merge-request refs, notes, and local-only branches are out of scope. The policy remains
configurable (`default` or `all`, with future include/exclude rules), and a capacity gate must run
before a deployment switches from `default` to `all`.

The logical source unit is a project commit snapshot:

```text
ProjectSnapshot {
    ProjectID
    PathWithNamespace
    RepoDir
    Commit
    BranchAliases[]
    Default
    Files[]
}
```

Branches in one project that point at the same commit share one snapshot and one tree walk; their
names remain aliases for filtering and provenance. The default branch alias is ordered first.
Freshness compares the new lock to the prior snapshot manifest at `(project ID, branch, commit)`
granularity. A force-push is simply a moved branch tip; a deleted branch removes that alias/snapshot
reference but does not immediately reclaim append-only CAS content.

### Git-object ingestion

For each distinct locked commit, ingestion uses `git ls-tree -r -z --full-tree` to enumerate paths
and blob object IDs, then `git cat-file --batch` to read required unique objects efficiently.
Content passes through the same text/binary classification, VCS-internal-path filtering, UTF-8 BOM
normalization, and content-true hashing used by the current CAS path. Tree entry modes (regular
file, executable, symlink, and gitlink) must be pinned by parity tests before cutover; nested gitlink
objects are never recursively indexed as ordinary file content.

The default checked-out branch is also ingested from its locked commit, not opportunistically from
mutable working-tree bytes. Unmanaged single-repository commands may retain their current
working-tree behavior; managed corpus builds are committed-snapshot builds.

### CAS and freshness model

Branch mode requires the content-addressable path ([0004](./0004-content-addressable-blob-store.md)).
The CAS manifest advances to a source-snapshot schema: project metadata plus commit snapshots,
branch aliases, and ordered file entries. Refresh processes only added/moved snapshots, uses
idempotent `Put` for net-new normalized content, carries unchanged snapshots verbatim, and retains
the prior snapshot on any ambiguous or transient ingest failure.

Content, token documents, symbols, and dense chunks remain blob-addressed, so identical content
across repositories, branches, and commits is indexed and embedded once. File/source reference
metadata scales with occurrences and is measured separately from unique content.

The legacy one-HEAD `manifest.json` and `blobmanifest.json` remain readable for default-branch
corpora. Branch-enabled writers emit new schema versions; there is no in-place reinterpretation of
old manifests.

### Source provenance and served format

The source contract becomes branch-aware. Every served match/context block can identify:

- stable GitLab project ID;
- `path_with_namespace` and leaf repository name;
- selected branch (or branch aliases when appropriate);
- exact commit;
- repository-relative path;
- a working-tree absolute path only when that checkout represents the exact locked default commit;
- a Git-object source locator for content that is not checked out.

The deduped shard format gains a new version (expected to be `MOEDEX06`) with normalized source
metadata so branch/commit strings are not repeated for every file reference. Readers remain
backward compatible with `MOEDEX03/04/05`; legacy references are interpreted as default-branch
sources with unknown project ID/commit where the old format cannot supply them.

`AbsPath` must never point at default-branch bytes for a non-default result. Internal grouping and
dedup keys use source identity plus relative path, not `AbsPath`, because non-checked-out sources do
not have a real filesystem path.

### Query semantics

Unscoped search remains **default-branch-only**. This is a compatibility and relevance contract:
enabling branch ingestion must not suddenly duplicate existing HTTP, CLI, or MCP results or admit
feature-branch-only content into queries that previously represented the deployed code line.

Exact and ranked APIs gain an explicit source scope. The first version supports project selection,
specific branch names, and an `include_non_default`/all-branches mode. Scope eligibility is applied
before result limiting and before ranked top-K, so a filtered query cannot underfill because
out-of-scope blobs consumed the candidate budget.

When one content blob has several eligible source references, default-branch provenance is preferred
for an unscoped query. A branch-scoped query selects a matching branch alias. Structured MCP and
HTTP output add project/branch/commit/source-locator fields; text output renders an unambiguous
`path_with_namespace@branch:path` header.

### Navigation boundary

LSP navigation remains a live/default-working-tree capability ([0017](./0017-lsp-navigation-and-the-serena-boundary.md)).
Moedex does not send a branch result to an LSP server reading another checkout. Navigation is
available only when the selected source matches the checked-out locked default commit. Temporary
branch worktrees and branch-specific LSP pools are a separate future decision.

## Consequences

**Positive**

- All selected remote branch tips become searchable without multiplying working trees.
- Git content dedup and incremental embeddings make the cost proportional mainly to unique changed
  blobs, not raw branch count.
- Every result has honest, reproducible project/branch/commit provenance.
- Default behavior and ranking quality remain stable until a caller explicitly requests branches.
- Branch moves and deletions become manifest deltas instead of full repository rebuilds.

**Negative / costs**

- The change crosses ingestion, CAS manifests, served persistence, exact search, ranking, context
  assembly, HTTP/MCP contracts, parity, and deployment. It is a format migration, not an isolated
  corpus feature.
- File-reference metadata can grow much faster than unique content when many branches share files.
- Fetching every branch tip may download substantial trees/blobs even with shallow ref fetches;
  capacity must be measured on the real TC corpus.
- Non-default results cannot be opened through an ordinary absolute path, and current LSP tools do
  not apply to them.
- A branch name is mutable human metadata; reproducibility depends on always returning the locked
  commit alongside it.

## Alternatives considered

- **One Git worktree per branch.** Rejected because it multiplies checkout storage, filesystem
  traversal, and lifecycle failure modes. The object database already contains the required trees.
- **Encode the branch in `Repo` or `RelPath`.** Rejected because it corrupts repository/path
  semantics, is ambiguous for legal branch names, and cannot safely support filtering or legacy
  clients.
- **Include every branch in unscoped results.** Rejected because shared content would create noisy
  duplicate provenance and feature-branch-only code would change existing search semantics.
- **Index only protected or recent branches.** Useful future policies, but not the initial semantic
  definition. The initial implementation measures all remote heads and keeps policy configurable.
- **Use the GitLab branches API as the indexing source.** Rejected as the content source: the corpus
  already must fetch Git objects, and exact locked commits plus local `ls-tree`/`cat-file` avoid one
  API request per branch and eliminate ref races during indexing.

## Evidence

- The current index and CAS already deduplicate identical content by content hash and attach
  multiple file references, which is the correct storage primitive for cross-branch reuse.
- The incremental embedding store keys chunks from blob/content identity, so unchanged branch
  content can reuse vectors once source metadata is separated from content identity.
- `contextwin.Assemble` currently reports `Files[0]`; `search.appendRefs` emits every file reference.
  Together they demonstrate why branch eligibility and provenance selection must be explicit at both
  exact-search and ranked-context layers.
- `MOEDEX05` serializes only repository, relative path, and absolute path for each file reference;
  persisted branch/commit provenance requires a new format rather than an in-memory-only field.

## Related

[0003](./0003-cox-reduction-ripgrep-parity.md),
[0004](./0004-content-addressable-blob-store.md),
[0011](./0011-shard-level-freshness.md),
[0015](./0015-structured-context-result.md),
[0016](./0016-incremental-embedding-refresh.md),
[0017](./0017-lsp-navigation-and-the-serena-boundary.md),
[0019](./0019-moedex-managed-submodule-corpus.md);
[implementation plan](../plans/0020-branch-aware-indexing.md).
