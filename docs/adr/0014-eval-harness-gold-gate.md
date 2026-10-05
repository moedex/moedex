# ADR 0014: Evaluation harness + hard gold gate — NDCG floor and a distraction-aware (UDCG) metric

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
Ranking ([0006](./0006-rrf-hybrid-ranking.md)) has four gated arms and a deferred learned reranker. Without a measured eval, every ranking change is a guess, gate tuning is folklore, and the reranker work has nothing to train or validate against — the research note ([`research/learned-reranker.md`](../../research/learned-reranker.md)) names a gold eval set as the blocker for *everything* past simple RRF. Standard IR metrics (NDCG/recall) reward putting relevant results high but say nothing about whether an agent's context window is **padded with plausible distractors** — which is the failure mode that matters when the consumer is an LLM ([0009](./0009-agent-context-api.md)).

## Decision
Build an in-repo evaluation harness (`internal/eval`) with a **hard CI gold gate**, and add a **distraction-aware metric (UDCG)** alongside the classic ones.

- Metrics: Recall@k, Precision@k, MRR, NDCG@k, **plus UDCG** (distraction-aware, arXiv 2510.21440) with hand-computed unit cases.
- **Hard gate**: load corpus labels and calibrated thresholds from `MOEDEX_EVAL_DATASET`; fail when an explicitly configured gate is malformed. Skip when no external dataset is configured.
- Gold set spans real strata: C#, TypeScript, SQL, ColdFusion, **non-filename-aligned** queries (to defeat the path arm's sampling bias), and a separately-measured **synonym-gap/agent-NL** stratum (where the dense arm earns its keep).

## Consequences
**Positive**
- Ranking changes are now measured, not asserted — the per-arm "purely additive" claim ([0006](./0006-rrf-hybrid-ranking.md)) is tested, and a regression fails CI.
- UDCG gives a metric for the agent-context failure mode (distractor padding) that NDCG is blind to — wired so it is invisible to the other metrics.
- The harness is the unblocking prerequisite the reranker work was gated on.

**Negative / costs**
- Relevance labels require independent review and representative query strata.
- UDCG is implemented and reported but **not yet a hard gate** (it's a soft/measured signal until calibrated) — promoting it to a floor is future work.
- Gold labels carry the usual annotator/LLM-judge subjectivity; the floors were deliberately recalibrated to honest values rather than aspirational ones.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0003](./0003-cox-reduction-ripgrep-parity.md), [0006](./0006-rrf-hybrid-ranking.md), [0007](./0007-optional-dense-arm.md), [0009](./0009-agent-context-api.md).
