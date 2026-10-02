# ADR 0031: Bound shards at file boundaries and close refresh dependencies

- Status: Accepted
- Date: 2026-09-30
- Predecessor: [0011](./0011-shard-level-freshness.md)

## Context

The first pinned Roslyn oracle run indexed 441.9 MB into one shard despite the
default 150 MiB target. Both the parity and production builders checked the
target only after ingesting an entire repository. The reported build peak was
8,459.5 MB. A large single repository therefore bypassed the intended limit.

Splitting a repository across shards also requires refresh to follow indirect
dependencies. If shards contain `A | B` and `B | C`, changing A requires consuming
both shards before reingesting B. Carrying the second shard would retain old B
files alongside newly indexed B files.

## Decision

Flush before adding a file that would exceed the current shard's byte target.
A single oversized file may occupy its own shard. Count bytes per file reference,
including repeated content at distinct paths. Apply this to parity builds,
production builds, and rebuilt shards.

Persist the target as optional `shard_bytes` in the existing freshness manifest.
Legacy manifests without it use the existing 150 MiB default when rebuilding.
Carried shards retain their existing bytes and boundaries.

Before carrying any old shard, compute the connected closure of affected
repositories and their shared shards. Reingest surviving repositories in that
closure, omit removed repositories, and carry unrelated shards unchanged.
Refresh need not reproduce a clean build's physical shard packing; every source
path and its bytes must agree without duplicate file references.

## Consequences and evidence

This bounds newly built shard content at file boundaries, not total process RSS.
Ingestion still materializes a repository's eligible files before indexing them.
Build-time maps, query state, and other allocations require separate measurement.
A connected chain of shared shards can require rebuilding several repositories.

Regression tests cover eager/selective builds, oversized files, repeated content,
overlapping refresh, exact surviving content and heads after removal, unchanged
disjoint shards, and legacy manifests. Targeted race tests and the full default
Go suite pass. Large-corpus measurements and their remaining limits belong in
[the public oracle record](../../research/semantic-intelligence/PUBLIC-CORPUS-ORACLE.md).
