# ADR 0001: Single-node scope, ~8 GB corpus, pure-Go zero-dependency default

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
moedex provides single-node search and agent context for caller-accessible source.
Required fleet coordination would add operational complexity to this scope.

## Decision
Scope moedex to a **single node**, an **internal-first** trust model, a **~8 GB corpus** envelope, and a **pure-Go standard-library default build with zero required external dependencies**.

- No multi-node distribution, replication, or cross-machine sharding in v1. Content addressing by git blob SHA (see [0004](./0004-content-addressable-blob-store.md)) deliberately leaves the seam open, but the design does not pay for distribution it does not need.
- The only optional externals are the dense-retrieval arm (see [0007](./0007-optional-dense-arm.md)) and the Zoekt differential oracle used by the parity gate; absent both, moedex builds and runs fully self-contained on the Go stdlib.
- Target deployment is one machine with ~8–16 GB RAM (see Evidence for the split).

**2026-08-25 amendment:** [0022](./0022-mcp-sdk-contract-and-snapshot-identity.md)
supersedes the "zero required external dependencies" clause of this decision for the MCP
surface. `github.com/modelcontextprotocol/go-sdk` v1.7.0 is a **required** dependency of
the default (untagged) build: `internal/mcp/sdkserver.go` carries no `//go:build`
constraint, and the SDK sits in `go.mod`'s untagged `require` block alongside
`github.com/google/jsonschema-go`. Adopting the SDK's maintained lifecycle, transport,
and schema handling was judged worth the dependency rather than re-implementing the MCP
wire contract in-tree.

Scope of the amendment, verified by `go list -deps`:

- `cmd/moedex-mcp`, `cmd/moedex-serve`, and `cmd/moedex-index` link **29 external module
  packages across 9 module roots**. The SDK reaches `moedex-index` transitively — the
  serving spine (`internal/server`) imports `internal/mcp`.
- `cmd/moedex`, `cmd/moedex-corpus`, and `cmd/moedex-parity` remain stdlib-only.

Everything else in this ADR stands unchanged: single-node scope, the ~8 GB corpus
envelope, the internal-first trust model, and the rule that the retrieval core adds no
required dependency of its own. The dense arm ([0007](./0007-optional-dense-arm.md)) and
the SIMD kernel ([0013](./0013-pure-go-defer-simd.md)) remain build-tagged and optional.

## Consequences
**Positive**
- One static binary, no cluster, no service mesh — trivial to deploy and operate.
- Zero required dependencies keeps the OSS-someday posture honest and the supply chain tiny.
- A single global blob-ID space and one in-process index removes a large class of cross-shard reconciliation bugs.

**Negative / costs**
- A hard single-node ceiling: the corpus must fit one machine's disk and the working set must fit its RAM.
- **The build, not serving, is the memory-binding step** (see [0005](./0005-mmap-compact-postings.md) and Evidence) — the corpus-wide sidecar phase is not shard-bounded.
- Growth past the envelope is a future project (distribution, or a compressed cold tier — [`research/fm-index-cold-tier.md`](../../research/fm-index-cold-tier.md), deferred until `corpus_bytes × ~2.8` exceeds disk or `× ~1.66` exceeds RAM).

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0004](./0004-content-addressable-blob-store.md), [0005](./0005-mmap-compact-postings.md), [0007](./0007-optional-dense-arm.md), [0013](./0013-pure-go-defer-simd.md).
