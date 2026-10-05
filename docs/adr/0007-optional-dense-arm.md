# ADR 0007: Optional dense arm — zero-dependency default, ONNX behind a build tag or a local HTTP server

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
The hybrid-ranking verdict ([0006](./0006-rrf-hybrid-ranking.md)) wants a dense semantic arm. But Sourcegraph **deprecated OpenAI embeddings for Cody** over privacy (code leaving to a third party), freshness complexity, and cost beyond ~100k repos — so the research read is "embeddings are *one arm of a local hybrid*, not the default semantic bet, and never sent off-box." Meanwhile the project's identity is a pure-Go, zero-required-dependency build ([0001](./0001-single-node-scope-pure-go-default.md)); a dense arm that drags in an ML runtime and a tokenizer by default would break that.

## Decision
Make the dense arm **optional and local**, behind the `embed.Embedder` interface and `nil`-able, with two interchangeable backends and a zero-dep default.

- **Default build links a no-op stub** (`internal/embed/onnx_disabled.go`) — the core keeps zero ML dependencies; `rank.New(ix, ti, nil, nil, cfg)` ranks with no externals.
- **In-process ONNX** (`embed.ONNXEmbedder`) compiled in only under `-tags onnx` — `st-codesearch-distilroberta-base`, 768-d, int8-quantized to ~78 MB embedded in the binary. This is the *only* thing that pulls `github.com/sugarme/tokenizer` and `github.com/yalue/onnxruntime_go` into `go.mod`.
- **Local HTTP** (`embed.HTTPEmbedder`) — any OpenAI/ollama-style `/embeddings` endpoint, no build tag, nothing leaves the box.
- Vectors are L2-normalized so cosine reduces to a dot product over a **flat brute-force `Store`** (`MDXE` codec); an ANN index is a later optimization behind the same `Search` API.

## Consequences
**Positive**
- The zero-dep posture survives: lexical + path + symbol ranking is fully self-contained; dense is strictly opt-in.
- Two backends cover both deployment shapes (single static binary with embedded model, or a sidecar embedding server) without code changes.
- The model is swappable via `NewONNXEmbedderFromFiles(...)`, so a newer embedder is an A/B, not a rewrite ([`research/code-embedders.md`](../../research/code-embedders.md) shortlists IBM `granite-embedding-english-r2` and others; deferred behind the gold set).

**Negative / costs**
- The float32 dense store is the memory outlier — ~2.78 GB at the 5.2 GB corpus (3.5× unique content), which is what pushes MCP-with-dense to ≥16 GB RAM unless int8-quantized ([0001](./0001-single-node-scope-pure-go-default.md)).
- Brute-force cosine is O(chunks) per query — fine at this corpus size, an ANN index is the eventual cost if the corpus grows.
- The bundled embedder is a ~2021 model; choosing a better one is real quality headroom, gated on a bigger eval set ([0014](./0014-eval-harness-gold-gate.md)).

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0001](./0001-single-node-scope-pure-go-default.md), [0006](./0006-rrf-hybrid-ranking.md), [0014](./0014-eval-harness-gold-gate.md).
