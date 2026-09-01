# ADR 0001: Single-node scope, ~8 GB corpus, pure-Go zero-dependency default

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
moedex is a clean-room successor to Zoekt aimed at one concrete workload: search and agent-context over the TurnCommerce source corpus. That corpus is `~/TCGitlab` ≈ **5.2 GB raw / 484 repos** today (60,883 indexed files, 953.6 MB of *indexed* text content), with planned headroom to ~8 GB. The original Zoekt is built for a multi-node, multi-tenant fleet; almost none of that machinery (cross-node sharding, replication, a shard manager, fleet RPC) is justified for a single team on a single box. The project also wants to be OSS-able and trivially operable, which makes every required third-party dependency a liability.

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
Scale run on the live 5.2 GB / 484-repo corpus (`cmd/scale`, `MOEDEX_MMAP=1`): 60,883 indexed files, 748,144,248 positional postings. Served-mode resident set under load ≈ **3.79 GB**; an mmap-reloaded index holds **~1.12× content** on the Go heap (postings stay off-heap — [0005](./0005-mmap-compact-postings.md)). Modeled 8 GB envelope: serve-HTTP and serve-MCP (lexical+symbol) sit comfortably under 8 GB RAM (irreducible heap ~1.7 GB); MCP **with** the float32 dense store needs ≥16 GB, or int8 quantization to fit 8 GB. The build-with-sidecars footprint peaked ≈ **10.35 GB** at 5.2 GB content — the binding constraint that sets the box's RAM, projected to ~16 GB at the 8 GB corpus target. The full-corpus parity build (6 shards) ran at peak RSS 6.4 GB (build) / 6.8 GB (run).

## Related
[0004](./0004-content-addressable-blob-store.md), [0005](./0005-mmap-compact-postings.md), [0007](./0007-optional-dense-arm.md), [0013](./0013-pure-go-defer-simd.md).
