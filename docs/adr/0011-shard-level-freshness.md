# ADR 0011: Shard-level freshness via a git-HEAD manifest — rebuild only affected shards

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
A warm daemon ([0010](./0010-warm-serving-spine.md)) over a prebuilt shard dir is stale the moment a repo advances. A full rebuild of the whole corpus per update is wasteful when only a few repos changed; the goal is the Blackbird "delta indexing" instinct — re-touch only what changed. But a content-sized shard interleaves several repos, so the granularity question (per-repo vs per-shard) is real.

## Decision
Track freshness with a **`manifest.json`** that records, per shard, which repos contributed blobs, and per repo its **git HEAD** at ingest. `moedex-index check` compares each repo's current `git rev-parse HEAD` against the manifest (`DetectChanges`); `refresh` rebuilds **only the shards whose repo set intersects the changed repos** (`Rebuild`), carries untouched shards forward byte-for-byte, atomically swaps the new dir into place, and rebuilds the token/symbol sidecars.

Freshness is **shard-level, not per-repo**, because a shard interleaves repos. The finer-grained, truly-incremental path lives at the CAS layer ([0004](./0004-content-addressable-blob-store.md)): `cas-refresh` appends only a changed repo's net-new blobs and leaves co-resident repos untouched.

## Consequences
**Positive**
- A localized change (one or a few repos in distinct shards) re-indexes only those shards, not the corpus.
- Atomic dir swap + SIGHUP gives a clean, observable refresh→serve cycle with no dropped requests.
- The CAS path ([0004](./0004-content-addressable-blob-store.md)) is the escape hatch when shard-level blast radius is too coarse.

**Negative / costs**
- **A broad update degenerates toward a full rebuild.** When a realistic `git pull` scatters changes across every shard, refresh re-ingests almost the whole corpus and fragments the shard set — measured below. This is the documented cost of shard-level (not per-repo) granularity, and the reason the CAS delta path exists.
- Carried + rebuilt shards lose cross-shard dedup between them (the deduped served format — [0004](./0004-content-addressable-blob-store.md) — is the durable fix); shard IDs are opaque per build.

## Evidence
Refresh validated on a 484-repo mutable copy after a real `git pull`: **89 repos advanced HEAD (~18% changeset)**. Because the changes scattered, refresh touched **all 6 shards, carried 0 forward**, re-ingested **480 of 484 repos (99%)**, and fragmented the shard set **6 → 480** (+2,515 blobs, +5.0% vs a clean full rebuild). Timing confirmed the degeneration: refresh **389 s** vs full rebuild **~410 s** — within noise. **Correctness PASS**: a spot battery of 8 literal+regex queries over the refreshed dir (480 shards) vs an independent full rebuild (6 shards) returned **identical match sets** (counts up to 51,698), and a SIGHUP swap under 400 concurrent requests dropped none. A real bug was fixed in passing: commitless repos (zero tracked files, no readable HEAD) were flagged "changed" on **every** check forever — `DetectChanges` now normalizes an unreadable HEAD to `""` to match `ingest.Head`, so a clean check reports no changes.

## Related
[0004](./0004-content-addressable-blob-store.md), [0010](./0010-warm-serving-spine.md).
