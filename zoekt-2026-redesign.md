# Redesigning Zoekt for 2026

> Deep-research synthesis on what a fresh-from-scratch version of Zoekt
> (Google/Sourcegraph's trigram-based code search engine) should keep, change,
> add, and subtract. Generated 2026-06-22.
>
> Method: fan-out web search across 5 angles → 20 sources fetched → 96 claims
> extracted → 25 adversarially verified (22 confirmed, 3 killed). Findings are
> tagged where they rest on primary evidence vs. inference.

## TL;DR

Keep the engine, replace the chassis. The **positional-trigram core is still
right** in 2026 — n=3 remains the proven sweet spot, and reducing regex to
boolean trigram queries is still the correct way to turn `/Google.*Search/`
into cheap candidate retrieval. What's dated is *everything around* that core:
shard layout, indexing scale, ranking, and the total absence of a first-class
story for LLM/agent consumption. The single highest-leverage change is adopting
GitHub Blackbird's content-addressable architecture; the second is moving from
"index every trigram + naive ranking" to workload-aware indexing + hybrid
lexical/dense ranking designed for an agent on the other end.

---

## What to KEEP (the parts that aged well)

1. **Positional trigrams (n=3).** Storing each trigram's offset lets a substring
   query intersect just *two* posting lists (begin-gram + end-gram) and verify
   positional distance — no per-file scan. Cox's reasoning holds: "too few
   distinct 2-grams and too many distinct 4-grams, so 3-grams it is."
   *(Correction surfaced: Zoekt stores rune offsets, not byte offsets.)*

2. **Regex → boolean trigram reduction.** Formal subexpression analysis
   (empty/exact/prefix/suffix sets per node, inference rules for
   concat/alternation/repetition) is the right candidate-retrieval strategy. In
   Cox's Linux-kernel example it narrowed 36,972 files → 25 (~100×, 1.96s → 0.01s).

3. **The favorable memory profile.** Positional trigrams need only ~1.2× corpus
   in RAM with posting lists on SSD; on-disk index ~3× corpus (2× offsets + 1×
   content). More attractive in 2026 with cheap NVMe, not less.

> ⚠️ Myth to kill: the "~20% of file size" figure is **Cox's presence-only
> index**, not Zoekt. Zoekt's *positional* index is far larger. Don't conflate
> them when sizing hardware.

---

## What to CHANGE

1. **Content-addressable sharding (biggest win).** Zoekt shards ~per-repo with
   `uint32` offsets capping shards at 4GB / content at 1GB. Blackbird shards **by
   Git blob SHA**: even distribution, no hot servers, identical blobs stored once.
   With **content dedup + delta indexing**, GitHub collapsed ~115TB raw → ~28TB
   unique, cut crawled documents >50%, and re-indexed the full corpus in **~18
   hours**. Foundational decision for a fresh build.

2. **64-bit offsets, larger shards.** The 4GB/1GB caps are a `uint32` artifact.
   Relax on modern 64-bit/large-RAM/NVMe hardware.

3. **Stop indexing every trigram.** All-trigram indexing hits ~0.97 precision but
   ~14.7s query time ("not cost-efficient"). Use **workload-aware n-gram
   selection**: frequency-based variable-length (FREE) cut build time 92% vs. the
   optimal BEST at only +1.2% query latency — ideal for streaming/incremental.
   Layer **bit-vector filter indexes (REI-style)**: ~2.1% extra storage for 14×
   (production) up to 379× speedups. Blackbird's "sparse grams" is the same
   instinct.
   *Caveat: FREE/BEST/REI were benchmarked on **log analysis**, not code search.
   Techniques transfer; exact multipliers may not.*

4. **Rethink ranking: BM25 first, hybrid dense second.** Sourcegraph
   **deprecated OpenAI embeddings for Cody** — privacy (code leaving to a third
   party), freshness complexity, "complex and resource-intensive" beyond 100k
   repos — and moved to native keyword search with **adapted BM25 + learned
   signals** (Zoekt already has `UseBM25Scoring`). But CodeRAG-Bench overturns the
   old "BM25 always wins on code" wisdom: dense models now **frequently surpass
   BM25** semantically, while BM25 still wins on exact identifiers. Verdict:
   **hybrid** — lexical/trigram for exact symbols, dense for semantic intent,
   fused. Not either alone.

---

## What to ADD

1. **A first-class agent/RAG context API.** Biggest gap in the original design.
   With oracle context, GPT-4o gained **+27.4% on SWE-Bench**, +6.9% on harder
   ODEX. But real systems are **bottlenecked on both ends** — retrievers fail to
   fetch useful context, generators fail to use it. Return **ranked,
   deduplicated, symbol-aware, token-budgeted context windows**, not raw grep hits.

2. **Compressed self-indexes (FM-index) as a serious option.** Infini-gram mini
   (EMNLP 2025) uses an FM-index that "simultaneously indexes and compresses text"
   for exact n-gram match at internet scale (46TB). Worth evaluating for the
   cold/archival tier.
   *Caveat: the "FM-index is only 44% of corpus" claim was **refuted 0-3** —
   don't sell it as strictly smaller than trigrams.*

3. **Symbol/semantic layer.** SCIP/LSIF precomputed symbols + tree-sitter
   language-aware indexing for go-to-def / find-references quality.
   *Transparency: this topic returned **no surviving verified claims** — it's
   synthesis from domain knowledge, flagged as an open question, not confirmed.*

---

## What to SUBTRACT

- **Per-repo sharding** → blob-SHA content addressing.
- **`uint32` offset constraints** → 64-bit.
- **"Index every trigram"** → selective n-gram + filter indexes.
- **Embeddings as the default semantic bet** → one arm of a hybrid, kept local.
- **The implicit "humans read results" assumption** → primary consumer is an LLM agent.

---

## Concrete opinionated design proposal

> Sharding/trigram/ranking pieces are evidence-backed (high confidence);
> symbol-layer, SIMD, and learned-index pieces are inference — no surviving
> claims covered them in this run.

1. **Storage:** Content-addressable store keyed by Git blob SHA, global dedup +
   delta indexing. 64-bit offsets, NVMe-resident posting lists, RAM holds only
   hot trigram maps.
2. **Index:** Positional trigrams as retrieval core; selective/sparse n-gram
   selection (FREE-style for the incremental path) + REI-style bit-vector
   pre-filters. Per-language tree-sitter pass emits a parallel **symbol index**
   (SCIP).
3. **Query:** Regex → boolean trigram reduction (unchanged). Three retrieval arms
   — exact (trigram), symbol (SCIP), semantic (local dense embeddings) — fused via
   rank fusion or a learned reranker.
4. **Ranking:** BM25 + learned signals as default; dense scores fused in. Tuned
   for *agent* relevance, not just lexical match.
5. **Serving:** An MCP/RAG endpoint returning token-budgeted, deduplicated,
   symbol-scoped context blocks — the index as an agent tool, not a grep server.
6. **Incremental:** Delta indexing targets hours-scale full rebuilds and
   near-real-time per-commit updates via the streaming n-gram path.

---

## Implementation language: would I still use Go?

**Yes — Go for the service, with a Rust (or C/SIMD) core for the hot loops. A
hybrid, leaning Go.**

**Why keep Go.** Go is why Zoekt is *maintainable and operable*: goroutine-per-
shard concurrency maps cleanly onto a sharded searcher, `mmap` + GC-light byte
slices fit the "posting lists on disk, scan in RAM" model, static binaries make
deployment trivial, and the surrounding ecosystem Zoekt lives in (Sourcegraph,
Git tooling, gRPC, Kubernetes operators) is Go-native. For the indexer,
crawler, shard manager, query planner, and serving layer, Go remains the
pragmatic default — none of those are CPU-bound enough to justify a harder
language.

**Where Go hurts, and what changes in 2026.** The redesign pushes harder on the
exact things Go is weakest at:

- **SIMD-accelerated posting-list intersection and candidate verification.** This
  is the inner loop, and it's where ripgrep-class performance comes from. Go's
  autovectorization is poor and hand-written SIMD means awkward assembly (`.s`
  files via Go's Plan9 assembler) or cgo. Rust (with `std::simd` / `std::arch`)
  or C is markedly better here. The original Zoekt left meaningful throughput on
  the table for this reason.
- **GC pauses and memory control at very large heaps.** Content-addressable
  sharding plus large-RAM hardware means bigger in-process structures; precise
  layout control (arenas, off-heap) is easier in Rust.
- **Dense/vector retrieval and reranking.** Likely calls into ONNX/llama.cpp/
  vector-index libraries that are C++/Rust anyway — Go would be an FFI client
  regardless.

**The 2026 call.** Go shifted the calculus *toward* keeping it: generics (1.18+)
remove a lot of the old `interface{}` boilerplate in index code, PGO improves
steady-state serving, and the toolchain/observability story is excellent. So:
write the **control plane, indexing pipeline, sharding, and serving in Go**;
isolate the **SIMD-heavy intersection/verification kernel** behind a clean
boundary and implement it in **Rust or C** (cgo or a subprocess/IPC worker) only
once profiling proves it's the bottleneck. Don't rewrite the whole thing in Rust
— you'd pay enormous ergonomic and ecosystem cost to speed up a small fraction of
the code. The honest summary: **Go for 90% of it, a native SIMD core for the
10% that's actually hot.**

*(This language section is engineering judgment, not a finding from the research
run — the deep-research pass did not cover implementation language.)*

---

## Caveats & open questions

- **Blackbird numbers are circa-2023**, not live 2026 scale.
- **Sourcegraph's "no embeddings" is era-specific** — they left the door open;
  2024–2025 systems reintroduce dense signals. Read as "embeddings aren't the
  *default*," not "never."
- **n-gram-selection / bit-vector results are from log-analysis benchmarks** —
  transfer to code is by analogy.
- **Genuinely uncovered by evidence:** SCIP/LSIF + tree-sitter specifics, learned
  indexes, concrete SIMD/NVMe exploitation. Strongest follow-up: *is precomputed
  symbol indexing better than on-the-fly tree-sitter at query time, and what's the
  right fusion (RRF vs. learned reranker) for the lexical+symbol+dense hybrid?*

**Refuted claims** (excluded from recommendations): "no n-gram strategy is
universally best" (0-3), "bigrams can beat trigrams for regex" (1-2), "FM-index
is only 44% of corpus" (0-3).

## Key sources

- [Zoekt design.md](https://github.com/sourcegraph/zoekt/blob/main/doc/design.md) — primary
- [Russ Cox, "Regular Expression Matching with a Trigram Index"](https://swtch.com/~rsc/regexp/regexp4.html) — primary
- [GitHub: The technology behind Blackbird code search](https://github.blog/engineering/architecture-optimization/the-technology-behind-githubs-new-code-search/) — primary
- [Sourcegraph: How Cody understands your codebase](https://sourcegraph.com/blog/how-cody-understands-your-codebase) — primary
- [CodeRAG-Bench (arXiv 2406.14497)](https://arxiv.org/pdf/2406.14497) — primary
- [n-gram selection: FREE/BEST (arXiv 2504.12251)](https://arxiv.org/abs/2504.12251) — primary
- [REI bit-vector filter index (arXiv 2510.10348)](https://arxiv.org/html/2510.10348v1) — primary
- [Infini-gram mini / FM-index (arXiv 2506.12229)](https://arxiv.org/abs/2506.12229) — primary
