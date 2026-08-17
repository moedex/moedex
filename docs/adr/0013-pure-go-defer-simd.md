# ADR 0013: Pure-Go execution; native SIMD kernel deferred (tried, no consistent win at this scale)

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
The research pass in [`research/simd-kernel.md`](../../research/simd-kernel.md) flagged SIMD-accelerated posting-list intersection and candidate verification as the inner loop where ripgrep-class throughput comes from, and where Go is weakest (poor autovectorization, awkward Plan9 assembly or cgo). The honest counsel was: **don't hand-write SIMD on a hunch** — profile first, exhaust pure-Go wins (intersect-smallest-first, buffer reuse, bitmaps), and only reach for a native kernel if intersection or verify clears a large share of query CPU after that. The dev machine is arm64; Go 1.26's `simd/archsimd` is amd64-only.

## Decision
Ship **pure Go by default on every architecture**, behind a clean kernel boundary, and **defer** a native SIMD kernel as the standing default — to be revisited only against a profile, not a hunch.

- `internal/setops` owns the sorted-uint64 set algebra (the trigram AND/OR fold); `internal/query` calls it instead of hand-rolling intersect/union. The pure-Go path uses galloping + caller-reusable buffers.
- An optional AVX2 kernel (`simd/archsimd`, no cgo, no `go.mod` dep) lives behind `-tags moedex_simd` (amd64 + `GOEXPERIMENT=simd`), built by `make build-simd` — present so the boundary is proven, not the default.

## Consequences
**Positive**
- One pure-Go binary, no cgo, no architecture-specific build for the default path — preserves the zero-dependency, trivially-portable posture ([0001](./0001-single-node-scope-pure-go-default.md)).
- The clean `internal/setops` boundary means a future native kernel is a drop-in behind a stable interface, not a rewrite.

**Negative / costs**
- The inherent latency tail ([0012](./0012-search-latency-positional-verify.md)) is left on the table where a verify-side SIMD prefilter *might* help — accepted, because that tail is scan-bound, not intersection-bound.
- Native-amd64 ns/op numbers and a Teddy-class SIMD verify prefilter remain unmeasured; the decision is "deferred," not "rejected forever."

## Evidence
The pure-Go galloping fold measured **~26× faster and zero-alloc** vs the old per-fold-allocating merge on the dominant rare-AND-common posting shape — the Tier-0 win the research note predicted, captured without SIMD. The optional AVX2 kernel was actually built and measured (Rosetta-translated x86-64, directional only): **no consistent win at this corpus scale** — the pure-Go galloping path beat the AVX2 broadcast-compare on the dominant skewed case. This confirms the latency analysis ([0012](./0012-search-latency-positional-verify.md)): the tail is scan-bound (Go's `regexp` machine over long minified lines), not intersection-bound, so an intersection SIMD kernel is low-leverage now.

## Related
[0001](./0001-single-node-scope-pure-go-default.md), [0012](./0012-search-latency-positional-verify.md).
