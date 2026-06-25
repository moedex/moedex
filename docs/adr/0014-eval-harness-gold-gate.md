# ADR 0014: Evaluation harness + hard gold gate — NDCG floor and a distraction-aware (UDCG) metric

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
Ranking ([0006](./0006-rrf-hybrid-ranking.md)) has four gated arms and a deferred learned reranker. Without a measured eval, every ranking change is a guess, gate tuning is folklore, and the reranker work has nothing to train or validate against — the research note ([`research/learned-reranker.md`](../../research/learned-reranker.md)) names a gold eval set as the blocker for *everything* past simple RRF. Standard IR metrics (NDCG/recall) reward putting relevant results high but say nothing about whether an agent's context window is **padded with plausible distractors** — which is the failure mode that matters when the consumer is an LLM ([0009](./0009-agent-context-api.md)).

## Decision
Build an in-repo evaluation harness (`internal/eval`) with a **hard CI gold gate**, and add a **distraction-aware metric (UDCG)** alongside the classic ones.

- Metrics: Recall@k, Precision@k, MRR, NDCG@k, **plus UDCG** (distraction-aware, arXiv 2510.21440) with hand-computed unit cases.
- **Hard gate** (`gold_gate_test.go`): production MeanNDCG ≥ 0.85 (currently ~0.93) and lexical-only ≥ 0.58 via `t.Errorf`; fails if the symbol arm attaches to zero blobs. Dense gates assert *additivity* but `t.Skip` when no ONNX runtime/corpus is present.
- Gold set spans real strata: C#, TypeScript, SQL, ColdFusion, **non-filename-aligned** queries (to defeat the path arm's sampling bias), and a separately-measured **synonym-gap/agent-NL** stratum (where the dense arm earns its keep).

## Consequences
**Positive**
- Ranking changes are now measured, not asserted — the per-arm "purely additive" claim ([0006](./0006-rrf-hybrid-ranking.md)) is tested, and a regression fails CI.
- UDCG gives a metric for the agent-context failure mode (distractor padding) that NDCG is blind to — wired so it is invisible to the other metrics.
- The harness is the unblocking prerequisite the reranker work was gated on.

**Negative / costs**
- The gold set is still **small** (~42 queries, 36 in the hard gate); the research note calls for 50–100 for a confident signal, so gold-set expansion is the standing next step before any reranker.
- UDCG is implemented and reported but **not yet a hard gate** (it's a soft/measured signal until calibrated) — promoting it to a floor is future work.
- Gold labels carry the usual annotator/LLM-judge subjectivity; the floors were deliberately recalibrated to honest values rather than aspirational ones.

## Evidence
`gold_gate_test.go` is a real `t.Errorf` gate, not a print — production MeanNDCG ~0.93 against a 0.85 floor. Concrete signals the harness produced: the path arm lifted lexical NDCG 0.64 → 0.89; path and symbol arms measured complementary (0.93 ≫ either alone); the synonym-gap stratum *reversed* an earlier "dense is net-negative" verdict (dense is +0.21 NDCG / +0.42 recall there), which is what justified the query-length-gated dense arm ([0007](./0007-optional-dense-arm.md)). UDCG has 9 hand-computed cases in `metrics_udcg_test.go`.

## Related
[0003](./0003-cox-reduction-ripgrep-parity.md), [0006](./0006-rrf-hybrid-ranking.md), [0007](./0007-optional-dense-arm.md), [0009](./0009-agent-context-api.md).
