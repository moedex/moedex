# Learned Reranker vs. RRF for moedex

> Research synthesis, 2026-06-22. Question: should moedex replace its current
> Reciprocal Rank Fusion (RRF, k=60) — fusing a BM25 lexical arm and an optional
> dense cosine arm in `internal/rank/ranker.go` — with a learned reranker tuned
> for *agent* relevance? If so, how, and in what order?
>
> Honesty note up front: most public effectiveness numbers below are from
> general RAG / text-and-table / TREC benchmarks, NOT code search specifically.
> Where a number is code-specific it is flagged. Treat the magnitudes as
> directional, not as promises for moedex's corpus.

---

## Recommendation (staged path)

**Do not rip out RRF. Stage it.** RRF and a learned reranker are not competitors
occupying the same slot — they sit at *different stages*. RRF (or convex
combination) is a recall/fusion stage; a learned reranker is a precision stage
that re-scores the top-N the fusion stage surfaced. The literature is consistent
that a reranker only pays off when the fusion stage already has the relevant docs
in a candidate pool of ~50–100; with too small a pool reranking is useless
because the right answer isn't there to promote.

Given moedex's constraints — pure-Go, single-node, **no labeled data, no query
log**, OSS-someday, quality-over-speed solo project — the evidence points to a
three-stage rollout, gated on actually being able to *measure* improvement:

- **Stage 0 (do first, cheap, high-confidence win): tune the fusion + add cheap
  features.** RRF k=60 is a default, not an optimum; one 2026 benchmark found
  convex combination (α=0.5) beat RRF, and among RRF variants k=10 beat k=60.
  Add engineered features moedex already has the data for (filename/path match,
  symbol match, path depth, exact-identifier boost, BM25/dense calibration). This
  is small, pure-Go, and reversible. It is also the only stage we can ship *today*
  without solving the no-labels problem.
- **Stage 1 (build the harness, not the model): an evaluation harness + a small
  gold set.** You cannot tell whether any reranker is better than tuned RRF
  without this, and shipping a reranker you can't measure is the main risk. Build
  ~50–100 gold queries with LLM-assisted labels (details below). This is the
  actual gating dependency for everything past Stage 0.
- **Stage 2 (only if Stage 1 shows headroom): a learned reranker.** Prefer, in
  cost order: (a) a GBDT/LambdaMART learning-to-rank model over the Stage-0
  engineered features, trained offline in Python (LightGBM) and run in pure Go via
  the `leaves` inference library — no cgo, no training pipeline in the binary; or
  (b) a small cross-encoder served over local HTTP, mirroring the existing
  embedder pattern, if (a) plateaus. A cross-encoder is the biggest measured
  precision lever in the literature but also the most data-hungry, highest-latency,
  and heaviest-infra option — justify it with eval numbers, not vibes.

The initial design already hedged this exact way: *"BM25 + learned signals as default;
dense scores fused in"* and the open question *"what's the right fusion (RRF vs.
learned reranker)..."*. The staged path resolves the open question empirically
instead of betting up front.

---

## Options table (cost vs. expected win vs. data needs)

| Option | Eng cost | Infra/latency | Data needed | Expected win | Pure Go? |
|---|---|---|---|---|---|
| **(0a) Tune fusion** (convex combo vs RRF, sweep k) | Trivial | None | A tiny eval set to pick params | Small but free; one benchmark: convex α=0.5 R@5 0.726 vs RRF-k60 0.695; RRF-k10 0.716 | Yes |
| **(0b) Cheap engineered features** (filename/symbol/path/exact-id, score calibration) | Low | None | Same eval set | Moderate on code (exact-symbol queries are where BM25 wins; filename/path are strong code priors) — *inference, not benchmarked here* | Yes |
| **(2a) GBDT / LambdaMART LTR** over engineered features | Medium (offline train in Python, infer in Go) | Negligible at query time (tree eval) | Hundreds–thousands of labeled (query, doc, grade) rows | Larger than hand-tuned weights when you have labels; standard LTR workhorse | Inference yes (`leaves`); training no (LightGBM/Python) |
| **(2b) Cross-encoder reranker** (small model over local HTTP) | High | +50–200ms for ~100 pairs; needs a served model | Thousands of pairs to fine-tune, OR use an off-the-shelf reranker zero-shot | Largest measured precision gain; e.g. one 2026 text/table study: +17.2pp MRR@3, +12.1pp Recall@5 over unreranked hybrid; general RAG nDCG@10 +5–15 (20+ on lexically hard sets); TREC '25 nDCG@10 +33.9% — *none code-specific* | Off-the-shelf zero-shot needs no training; served like the embedder |

Candidate-pool sizing is a hard prerequisite for (2a)/(2b): reranking ~20
candidates was found ineffective (R@5 0.458), jumping to 0.826 at 50 and 0.888 at
100. moedex's dense arm already pulls 64 chunks (`store.Search(..., 64)`) and the
lexical arm is unbounded candidates — so the pool is fine; just make sure the
fused top-N handed to a reranker is ≥50.

---

## The no-labeled-data problem and where signal comes from

This is moedex's binding constraint: no clicks, no query log, no relevance
judgments. Realistic signal sources for a solo OSS project, roughly in order of
effort/reliability:

1. **Heuristic / distant labels (free, weak).** Treat strong code priors as
   pseudo-labels: a query that is an exact symbol name → the blob defining that
   symbol is relevant; a query matching a filename → that file is relevant;
   test/impl co-location. These are noisy but cheap and directly become both
   features (Stage 0b) and weak labels (Snorkel-style noisy labelers, where each
   heuristic is a labeling function and a weak-supervision model learns reliability
   weights). Good enough to *bootstrap*, not to be the final word.
2. **LLM-as-judge synthetic labels (moderate, the realistic backbone).** This is
   how the field now substitutes for human annotation: an LLM scores (query, code
   chunk) relevance pointwise on a rubric, producing graded labels at near-zero
   marginal cost. Microsoft uses GPT models for Bing relevance assessment;
   weakly-supervised LTR over LLM labels is an established 2025 pattern. moedex can
   reuse its existing local-HTTP model plumbing to call a judge model. Caveats the
   literature is loud about: a *self-referential ceiling* (the judge defines
   "relevant," so you can't beat the judge's notion), cognitive/positional biases,
   and one bluntly-titled 2025 paper *"Don't Use LLMs to Make Relevance
   Judgments."* Mitigations: ensemble multiple judges/prompts, validate the judge
   against a tiny human-checked slice, use it for the *easy* labeling decisions.
3. **Agent task outcomes as reward (highest fidelity, highest effort).** The
   gold-standard signal for "agent relevance" is: did retrieving this context let
   an agent resolve the task? SWE-bench-style pass/fail, or expert-annotated *gold
   contexts* (ContextBench, SWE-Context-Bench, early 2026) where humans mark the
   repository artifacts genuinely needed to fix an issue. This is the right
   *evaluation* target and an aspirational training reward, but curating it solo is
   expensive. Use a handful of these tasks as a held-out sanity check, not as the
   training corpus.

Practical synthesis for moedex: **heuristic labels to bootstrap features +
LLM-judge to grade a small gold set + a few SWE-bench-style tasks as held-out
truth.** Do not wait for "real" labels that will never arrive.

---

## "Agent relevance" specifically — and how it changes features/eval

Ranking for an LLM agent is genuinely different from ranking for a human, and the
2025 literature is now explicit about it (this is the doc's *"bottlenecked on both
ends"* — retrieval fails to fetch useful context AND generators fail to use it;
with oracle context GPT-4o gained +27.4% on SWE-Bench):

- **The consumer reads everything at once, not top-down.** Humans scan a ranked
  list with monotonically decreasing attention; rank-position-weighted metrics
  (nDCG/MRR) encode that. An LLM ingests the whole context window jointly. ("Redefining
  Retrieval Evaluation in the Era of LLMs," arXiv 2510.21440, Oct 2025.)
- **Irrelevant results actively harm, they aren't ignored.** LLMs lack the human
  filter for noise; irrelevant-but-plausible passages are *distractors* that
  degrade the answer. Worse, irrelevant passages surfaced by a *strong* retriever
  are MORE distracting than those from a weak one (they look more on-topic). "Hard
  distractors" cost up to ~9 accuracy points vs weak ones. Implication: precision
  at the *top of the context window* matters more for an agent than for a human,
  because a human just skips the dud and an agent may anchor on it. That paper
  proposes **UDCG (Utility and Distraction-aware Cumulative Gain)** — reward
  relevant-and-useful, *penalize* distractors — instead of nDCG.
- **But recall is still favored overall for coding agents.** ContextBench-style
  work finds that across LLMs and agents, higher recall is consistently preferred
  over precision for *resolving* an issue (you must include every file the patch
  touches), even though distractor-sensitivity says don't pad with junk. The
  reconciliation: maximize recall of the *truly needed* artifacts while minimizing
  hard distractors — exactly what a distraction-aware reranker + token budgeting
  buys you.

What this changes for moedex:
- **Features:** add symbol/definition-awareness (a chunk that *defines* the queried
  symbol > one that merely mentions it), and treat the dense arm's "semantically
  related but not the answer" hits as candidate *hard distractors* to down-rank,
  not automatically promote.
- **Eval:** prefer a distraction-aware / utility metric (UDCG-style) and, where
  affordable, end-task success (did the assembled context window let an agent
  solve a held-out task?) over bare nDCG@10. moedex already returns a
  token-budgeted, deduped context window via `internal/contextwin` + the MCP
  `search_context` tool, so the natural unit of evaluation is "quality of the
  assembled window," not "rank of a single hit."

---

## Integration points in `internal/rank` (and the local-HTTP reranker mirror)

Current shape (`internal/rank/ranker.go`): `Rank()` builds `lexicalArm()` (BM25
over trigram candidates, sorted desc) and `denseArm()` (cosine over chunk
embeddings, top 64, sorted desc), then fuses by RRF into `byBlob` aggregates and
emits `RankedResult{Blob, Files, Score, Lexical, Dense, LineSpans}`. `rank.go`
freezes `RankedResult`/`LineSpan` as a contract that `internal/contextwin`
depends on — so the reranker must preserve that output shape and only change how
`Score` (and ordering) is produced.

Clean insertion points, least to most invasive:

- **Stage 0a (fusion swap):** the fusion block (`ranker.go` ~lines 70–101) is the
  only thing that needs to change. Add a `Fusion` enum / weights to `Config`
  (alongside `RRFk`) so RRF vs convex-combination and the k-sweep are config, not
  a fork. `RankedResult` is untouched.
- **Stage 0b (features):** `RankedResult` already carries `Lexical` and `Dense`
  raw components — extend the internal `agg` struct with engineered features
  (filename-match, symbol-match, path-depth, exact-identifier flag) computed from
  `r.ix.Blob(blob).Files` and the query terms. Combine via tuned linear weights.
  Still pure Go, still preserves the output contract.
- **Stage 2a (GBDT/LambdaMART):** introduce a `Reranker` interface that takes the
  fused top-N `[]RankedResult` + the per-result feature vector and returns a
  reordered slice. A `GBDTReranker` loads a LightGBM `lambdarank` model file at
  startup via `github.com/dmitryikh/leaves` (pure Go, no cgo) and calls
  `PredictSingle(featureVector)` per candidate, then re-sorts. Training happens
  offline in Python; only the exported model text file ships. This keeps moedex's
  "pure-stdlib + one local HTTP dep" posture intact (the `leaves` dep is the first
  non-stdlib Go lib — weigh that against OSS-someday).
- **Stage 2b (cross-encoder over HTTP):** add a `CrossEncoderReranker`
  implementing the same `Reranker` interface, backed by a struct that mirrors
  `embed.HTTPEmbedder` almost exactly: a `baseURL`/`model`/`http.Client`, POSTing
  (query, candidate-text) pairs to a local rerank endpoint and parsing scores —
  the same "fake for tests, real server in prod" pattern `embed.go` already uses
  (CONTRACT FREEZE comment, lines 16–28). The reranker stays *optional* exactly
  like the dense arm: nil interface → pure fused ranking, zero external deps. This
  is the cleanest reuse of an existing, blessed pattern in the codebase.

So the recommended abstraction is a single optional `Reranker` interface in
`internal/rank`, with three concrete implementations available over time
(identity/none, GBDT, cross-encoder-HTTP), selected by config — and `Rank()`
calls it as a post-fusion step on the top-N before truncating to `topK`.

---

## Evaluation plan (build this BEFORE any reranker)

1. **Gold set:** 50–100 representative queries against moedex's own corpus
   (mix: exact-symbol, natural-language intent, multi-file/feature queries). For
   each, label relevant blobs/spans. Bootstrap labels with heuristics + an
   LLM-judge (local HTTP), then hand-verify a slice to estimate judge noise.
2. **Metrics, in priority order for an agent consumer:**
   - **UDCG-style utility/distraction metric** (reward relevant, penalize hard
     distractors) — the agent-aligned primary.
   - **Recall@k of needed artifacts** (ContextBench finding: recall is favored).
   - nDCG@10 / MRR as familiar secondary baselines for comparability.
   - **End-task proxy:** for a handful of SWE-bench-style held-out tasks, does the
     assembled context window (the MCP `search_context` output) contain the gold
     context? This catches "looks good by IR metric, useless to the agent."
3. **Protocol:** freeze the gold set; run tuned-RRF (Stage 0) as the baseline;
   every later stage must beat it on the primary metric by a margin larger than
   the judge-noise estimate, or it doesn't ship. Keep the harness in-repo
   (`internal/rank` test fixtures + a small offline eval command) so regressions
   are caught.

---

## Minimal first slice

Ship **Stage 0 + the eval harness**, nothing more:
1. Add `Fusion` mode + weight config to `rank.Config`; implement convex
   combination alongside RRF; sweep k and α on the gold set.
2. Add 2–3 cheap engineered features (filename match, exact-identifier match,
   path depth) into the fusion score.
3. Build the 50–100-query gold set with LLM-judge + heuristic labels and a small
   offline eval command reporting UDCG-style + recall@k + nDCG@10.
4. Record the tuned-RRF baseline number. **Stop.** Only proceed to a GBDT or
   cross-encoder reranker if the eval shows the fusion stage has surfaced relevant
   docs but is mis-ordering them (i.e. recall is high, precision/UDCG is the gap)
   — that's the precise signature that says "a precision reranker will help."

---

## Risks & honest caveats

- **Evidence transfer:** the headline reranker gains (+17pp MRR, +33% nDCG,
  +5–15 nDCG) are general-RAG / text-table / TREC, not code search. The CodeRAG-Bench
  reranking numbers the redesign doc gestures at did not surface cleanly in this
  run; treat code-specific reranker uplift as *plausible but unproven for moedex*.
- **No labels is the real blocker, not the algorithm.** Any learned model is only
  as good as the labels; LLM-judge labels carry a self-referential ceiling and
  documented biases ("Don't Use LLMs to Make Relevance Judgments," 2025). The eval
  harness can be poisoned by the same judge it trains against — keep a
  human-checked anchor slice.
- **Dep / OSS cost:** `leaves` would be moedex's first non-stdlib Go dependency
  (pre-1.0 API, minor float divergence from the C reference); a cross-encoder adds
  a second served model alongside the embedder. Both are acceptable per the stated
  constraints but are real additions to the "pure stdlib + one HTTP dep" identity.
- **Latency:** cross-encoders add 50–200ms per ~100 pairs. Fine for a
  quality-over-speed side project; note it anyway.
- **Over-engineering:** the literature's own framing is that RRF is a *good*
  first-stage reranker and the win is conditional on recall already being high.
  It's entirely possible Stage 0 (tuned fusion + cheap features) closes most of
  the gap and a learned reranker is never justified for an 8GB single-node corpus.
  Let the eval decide.

---

## Sources (URL — claim — date)

- [Redefining Retrieval Evaluation in the Era of LLMs (arXiv 2510.21440)](https://arxiv.org/html/2510.21440) — LLMs consume context jointly not top-down; irrelevant passages are active distractors and strong-retriever distractors hurt more; proposes UDCG (utility + distraction-aware); hard distractors cost up to ~9 acc points. Oct 2025.
- [ContextBench: A Benchmark for Context Retrieval in Coding Agents (arXiv 2602.05892)](https://arxiv.org/pdf/2602.05892) — expert-annotated "gold contexts"; across LLMs/agents higher recall is consistently favored over precision; SWE-bench-style pass@k ignores *how* agents get context. Early 2026.
- [SWE Context Bench (arXiv 2602.08316)](https://arxiv.org/pdf/2602.08316) — context-learning benchmark for coding agents. Early 2026.
- [From BM25 to Corrective RAG: Benchmarking Retrieval Strategies for Text-and-Table Documents (arXiv 2604.01733)](https://arxiv.org/pdf/2604.01733) — RRF beats both constituents; convex combination α=0.5 (R@5 0.726) beats RRF-k60 (0.695); RRF-k10 best variant (0.716); cross-encoder rerank adds +17.2pp MRR@3, +12.1pp R@5; rerank needs ~50–100 candidate pool (R@5 0.458→0.826→0.888). 2026. *Text-and-table, not code.*
- [leaves — pure-Go GBRT/LightGBM/XGBoost inference (pkg.go.dev / github dmitryikh/leaves)](https://pkg.go.dev/github.com/dmitryikh/leaves) — load LightGBM `lambdarank` model file, `PredictSingle` per candidate, no cgo; pre-1.0 API, minor float divergence. Accessed 2026-06-22.
- [Weak Supervision for Improved Precision in Search Systems (arXiv 2503.07025)](https://arxiv.org/pdf/2503.07025) — weak supervision as scalable alternative to manual labels; LLMs as labeling functions for simpler labeling tasks. Mar 2025.
- [Rankers, Judges, and Assistants (SIGIR 2025, Balog et al.)](https://krisztianbalog.com/files/sigir2025-llms.pdf) — LLM-as-judge as an LTR instantiation (pointwise/pairwise/listwise); three research lines incl. synthetic test collections; bias caveats. 2025.
- [Principles and Guidelines for the Use of LLM Judges (Dietz, 2025)](https://www.cs.unh.edu/~dietz/papers/dietz2025principles.pdf) — self-referential ceiling, diversity concerns, when LLM judges are/aren't trustworthy. 2025.
- [RAG Reranking with Cross-Encoders (BigData Boutique)](https://bigdataboutique.com/blog/rag-reranking-improving-retrieval-quality-with-cross-encoders) — funnel pipeline; nDCG@10 +5–15 (20+ on lexically hard sets) under ~200ms; retrieve 50–100, rerank to 10. Accessed 2026.
- [SPENCER: Self-Adaptive Model Distillation for Efficient Code Retrieval (arXiv 2508.00546)](https://arxiv.org/pdf/2508.00546) — cross-encoder reranks dual-encoder code candidates for accuracy; cross-encoder cost is the limiter at codebase scale. Aug 2025.
- [Architecture design lineage](../ARCHITECTURE.md#design-lineage) and [ADR 0006](../docs/adr/0006-rrf-hybrid-ranking.md) — hybrid lexical/dense design, the RRF decision, and the learned-reranker boundary.
