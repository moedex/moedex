# ADR 0016: Incremental embedding refresh — content-keyed vector reuse

- **Status:** Accepted
- **Date:** 2026-06-26
- **Context owner:** moedex

## Context
The dense arm ([0007](./0007-optional-dense-arm.md)) is the expensive part of a refresh. Shard/token/symbol freshness ([0011](./0011-shard-level-freshness.md)) rebuilds in seconds, and the out-of-band builder (`moedex-serve -build-embeddings`) keeps the embed off the daemon's reload path — but it was still **all-or-nothing**: any corpus change shifted the shard fingerprint, invalidated the persisted store, and re-embedded the **entire** corpus (~896k chunks, ~1h45m on this machine). A daily refresh that touches a handful of repos paying a full re-embed is the dominant cost in the freshness pipeline.

The blocker to reuse: a chunk's stored identity was its **blob ID + byte span**, but blob IDs are *positional* — reassigned by shard-concatenation order on every build ([0011](./0011-shard-level-freshness.md)) — so a stored vector could not be matched back to a chunk in the next build, even when the underlying text was byte-identical.

## Decision
Give every chunk a **content key** — `sha256(chunk text)[:16]` — persisted alongside its vector (embedding-store format **v2**). The key depends only on the bytes that determine the embedding, so it is stable across blob-ID churn, repo reordering, and shard refragmentation.

`BuildStoreIncremental` walks the new corpus, computes each chunk's key, and **reuses the prior store's vector wherever the key matches**, embedding only genuinely new text (de-duplicating identical new text within a build). `server.RefreshEmbeddings` orchestrates the decision:

- **up to date** (fingerprint matches, store already keyed) → nothing to do;
- **legacy keyless store, still current** → re-key in place, no re-embed (`FillKeys`);
- **corpus changed** → incremental rebuild reusing unchanged vectors;
- **different embedding model** → no reuse (a model's vectors are incomparable) — a clean full re-embed, gated on the store meta's recorded model.

Reuse is **byte-identical** to a fresh embed for every carried-over chunk (the embedder is deterministic for identical text), so ranking does not move. The store is written **atomically** (temp + rename) so a torn multi-GB write can never replace a valid sidecar with a truncated one.

## Consequences
**Positive**
- A refresh that touched a few repos re-embeds only those chunks — minutes, not ~1h45m — keeping the daily timer cheap.
- Content keying is immune to positional blob-ID churn, so it composes with both freshness paths ([0011](./0011-shard-level-freshness.md), [0004](./0004-content-addressable-blob-store.md)).
- Atomic save hardens the refresh against partial writes (a real risk after the 2026-06-26 index-loss incident).

**Negative / costs**
- +16 bytes/chunk on disk (~14 MB at 896k chunks; <0.6% of the store).
- Every refresh hashes the whole corpus text to compute keys (~seconds, memory-bandwidth bound) — negligible beside even a 1% re-embed.
- One transition cost: a pre-existing v1 (keyless) store must be re-keyed once before reuse kicks in. The re-key is free (no embedding) and the loader stays back-compatible with v1.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0007](./0007-optional-dense-arm.md), [0011](./0011-shard-level-freshness.md), [0004](./0004-content-addressable-blob-store.md), [0010](./0010-warm-serving-spine.md).
