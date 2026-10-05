# ADR 0006: Hybrid multi-arm ranking fused via RRF — not a learned reranker

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
Grep returns matches in scan order; an agent needs them **ranked**. CodeRAG-Bench overturned the old "BM25 always wins on code" wisdom — dense models now frequently surpass BM25 semantically, while BM25 still wins on exact identifiers — so the initial research verdict was **hybrid: lexical for exact symbols, dense for intent, plus filename and symbol signals, fused.** Fusing arms whose scores live on different scales (BM25 magnitudes vs cosine in [0,1] vs coverage fractions) needs either careful score calibration or a rank-based fusion that sidesteps it. A learned reranker is the higher ceiling but needs a labeled gold set to train and tune against — which did not exist when ranking was first built. The design lineage and source links are retained in [`ARCHITECTURE.md`](../../ARCHITECTURE.md#design-lineage).

## Decision
Fuse up to four independent retrieval arms with **Reciprocal Rank Fusion** (RRF, each arm contributing `1/(RRFk + rank)`, `RRFk = 60`), in `internal/rank/ranker.go`. Each arm is **independently gated so it only ever adds signal, never subtracts**:

- **Lexical (BM25)** — always on (`K1 = 1.2`, `B = 0.75`), over the persistent token index (`internal/tokenindex`). Candidate blobs come from trigram postings, or from the token index (`UseTokenCandidates(true)`) in the content-only corpus ranker.
- **Path / filename** ([0009-adjacent], `pathArm`) — **on by default**; a blob whose path tokens cover ≥ `PathMinCoverage` (0.6) of the query's distinct terms votes. Needs no external index (every blob has a path).
- **Symbol-name** (`symbolArm`) — on when a symbol index is attached ([0008](./0008-polyglot-symbol-sidecar.md)); a blob whose *defined* symbol names cover ≥ `SymbolMinCoverage` (0.67) votes, so `func Refund` outranks a mere mention.
- **Dense** ([0007](./0007-optional-dense-arm.md), `denseArm`) — on by default but **query-length gated** (`DenseMinQueryTerms = 5`): reserved for longer natural-language queries where cosine adds signal, off for short keyword queries that are the lexical/symbol/path arms' home turf.

A learned reranker (GBDT/LambdaMART or cross-encoder) is **explicitly deferred** behind the gold-set/eval work ([0014](./0014-eval-harness-gold-gate.md)), per [`research/learned-reranker.md`](../../research/learned-reranker.md).

## Consequences
**Positive**
- RRF needs no cross-arm score calibration — it is the conservative, robust default, and the per-arm gates keep every arm purely additive (proven on the gold set, not assumed).
- The path and symbol arms are pure-Go and dependency-free, so the strongest ranking signals ship in the zero-dep build.

**Negative / costs**
- RRF leaves precision on the table vs a tuned reranker; that headroom is real but unbuilt, and chasing it is gated on a bigger gold set ([0014](./0014-eval-harness-gold-gate.md)) — the prerequisite the research note names.
- Four gates means four knobs to keep honest; the gold gate (`gold_gate_test.go`) is what stops a regression from a mis-set gate.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0007](./0007-optional-dense-arm.md), [0008](./0008-polyglot-symbol-sidecar.md), [0009](./0009-agent-context-api.md), [0014](./0014-eval-harness-gold-gate.md).
