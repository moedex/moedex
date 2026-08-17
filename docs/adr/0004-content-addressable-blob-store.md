# ADR 0004: Content-addressable storage keyed by git blob SHA — global dedup, per-blob delta, deduped served format

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
Zoekt shards roughly per-repo, with `uint32` offsets capping a shard at 4 GB / content at 1 GB. The initial research pass identified the single highest-leverage change as **GitHub Blackbird's content-addressable architecture**: shard by git blob SHA so identical content is stored once. With content dedup + delta indexing, GitHub collapsed ~115 TB raw → ~28 TB unique. A polyglot corpus like `~/TCGitlab` (vendored libraries, forks, copied config) has heavy cross-repo and cross-shard content duplication that per-repo sharding pays for repeatedly. The source and its historical-scale caveat are retained in [`ARCHITECTURE.md`](../../ARCHITECTURE.md#design-lineage).

## Decision
Address all content by **git blob SHA** and store each unique blob **once corpus-wide**, with a per-blob delta refresh path and a deduped served format.

- **In-index dedup** (`internal/index`): `AddFile` indexes identical content once and accumulates `FileRef`s, so one blob can back several paths.
- **Corpus-wide CAS** (`internal/blobstore`, `MOEBLOB1`): an append-only `blobs.pack` (one record per *unique* blob) + atomically-written `blobs.idx`. Idempotent `Put` is the cross-shard dedup primitive; `cas-refresh` re-ingests only a changed repo's net-new blobs, leaving co-resident repos untouched. The SHA is an opaque variable-length key (SHA-1 today, SHA-256-ready).
- **Deduped served format** (`internal/diskstore`, `MOEDEX05` content-less shards + one shared `blobs.dat` `MOECONT1` store): `cas-export -deduped` writes shards that carry SHAs + file-refs but **no inlined content**; content lives once in the shared store and is resolved at load as a zero-copy mmap sub-slice.

## Consequences
**Positive**
- Each unique blob's content is stored once for the whole served corpus (footprint ≈ the CAS `StoredBytes`), not re-inlined per shard.
- A per-repo refresh becomes a pure set-diff: the `blobmanifest.json` records each repo's blob set, so `cas-refresh` physically appends only net-new blobs.
- Leaves the distribution seam open ([0001](./0001-single-node-scope-pure-go-default.md)) without paying for it now.

**Negative / costs**
- Two silent-failure risks, both guarded: the shared store is **self-verifying** at open (every entry re-hashed against its content-addressed key, so corruption fails the boot; default-on, `MOEDEX_VERIFY_CONTENT=0` opts out), and the rank-sidecar fingerprint **folds in a content-true hash of `blobs.dat`'s directory** so a content change invalidates stale token/symbol/embedding caches even when shard files are byte-unchanged.
- The append-only pack grows monotonically — **GC/compaction of blobs no longer referenced by any repo is deferred** (the manifest's referenced-set is the liveness signal a future compactor needs).
- The non-`-deduped` `cas-export` still writes the inlined `MOEDEX03` bridge, kept as the proven default; a delta-aware deduped *re-export* (append only net-new content + rewrite only affected shards) exists but the legacy `moedex-index refresh` path is still shard-level ([0011](./0011-shard-level-freshness.md)).

## Evidence
`cas-build` prints the dedup ratio (raw / stored bytes) directly on the live corpus. The deduped served path is proven byte-for-byte parity-clean against the direct inlined build and ripgrep by the deduped arm of `blobstore.TestCASExportParityCorpus` — `server.Corpus`/`RankCorpus` resolve content from the one shared mmap'd store and return identical `(file, line)` matches. The manifest write fsyncs the completeness marker so a hard crash never indexes non-durable bytes.

## Related
[0001](./0001-single-node-scope-pure-go-default.md), [0005](./0005-mmap-compact-postings.md), [0010](./0010-warm-serving-spine.md), [0011](./0011-shard-level-freshness.md).
