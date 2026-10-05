# Newer Code Embedders for moedex's Dense Arm

> Research synthesis, 2026-06-24. Question: moedex's dense retrieval arm bundles
> `st-codesearch-distilroberta-base` (DistilRoBERTa, CodeSearchNet, ~2021;
> 768-d, int8 ~78MB), run in-process via the ONNX Runtime Go binding with
> mean-pooling + L2 norm. The model is stale. What 2025-2026 code/text embedder
> should replace it, and via which integration path?
>
> Honesty note up front: every benchmark number below is from CoIR or MTEB-Code
> (public code-retrieval suites), NOT moedex's TC corpus. They rank-order
> candidates; they do not predict moedex's NDCG. In particular **no public
> benchmark covers ColdFusion**, which is a meaningful slice of the TC corpus.
> Treat the numbers as a shortlist filter, not a promise. The only thing that
> settles it is `TestCorpusCodeModelMeasurement` on our gold set (steps below).

---

## Recommendation (staged A/B)

**A/B these in order, all through the existing `TestCorpusCodeModelMeasurement`
harness — change the model, not the code.** The dense arm's job is narrow and
already established by eval work: it is *gated* to fire only on long
natural-language / synonym-gap queries (`rank.Config.DenseMinQueryTerms=5`),
where it lifts NDCG ~0.14→0.34 and recall ~0.08→0.50. We are optimizing
**recall of the right code chunk for paraphrase / NL queries over a polyglot
legacy corpus**, nothing else. So the bar is: clearly beat the 2021 baseline on
the agent-NL stratum, while staying a clean in-process drop-in.

1. **First A/B — `granite-embedding-english-r2` (IBM, 149M, ModernBERT, 768-d,
   Apache-2.0).** This is the only candidate that is *both* a strong 2025 code
   embedder *and* a frictionless in-process drop-in: it is an **encoder** (emits
   `last_hidden_state [batch,seq,dim]`), it is **RoBERTa-style** (`input_ids` +
   `attention_mask`, **no `token_type_ids`** — exactly the harness default), it
   has official Apache-2.0 ONNX weights, 768-d matches the current store, and it
   was trained with explicit code coverage incl. **SQL** (Python/Go/Java/JS/PHP/
   Ruby/SQL/C/C++ in the code-retrieval set). The one caveat to handle: Granite
   pools **CLS** (`[:,0]`), while moedex mean-pools. See "CLS vs mean" below — you
   either patch the pooler or accept mean-pool as an approximation in the A/B.
   Same family also has a 768-d `granite-embedding-311m-multilingual-r2` if the
   English one underperforms on the non-English-comment legacy code.

2. **Second A/B — `Qwen3-Embedding-0.6B` (Alibaba, 0.6B, 1024-d, Apache-2.0)
   over the HTTP path.** Top-of-leaderboard general embedder with real code
   retrieval, Apache-2.0, and an *official* instruction-aware design — but it is
   a **decoder LLM with last-token pooling + left-padding + a required query
   instruction prefix**, which the in-process mean-pool path will get *wrong*.
   Serve it (llama.cpp / Ollama / vLLM expose an OpenAI `/embeddings` endpoint)
   and point `embed.NewHTTPEmbedder` at it. This tests "does a much stronger,
   instruction-aware model beat a code-specialized encoder on our NL stratum,
   even at 1024-d and the cost of a sidecar service?"

3. **Only if #1 underwhelms and we want a code-specialized encoder bigger than
   Granite — `codesage-large-v2` (Amazon, 1.3B, 2048-d, Apache-2.0).**
   Encoder, Apache-2.0, Matryoshka (can truncate 2048→smaller). Costs:
   `trust_remote_code=True` (custom modeling — verify the ONNX export is clean),
   2048-d doubles index size + brute-force cosine cost, and it only covers 9
   languages (no SQL, no ColdFusion). Treat as a stretch candidate.

**Do not adopt the CoIR/CoIR-topping models that are non-commercial.** The
literal CoIR leaders (`SFR-Embedding-Code`/CodeXEmbed, `jina-code-embeddings`)
are **CC-BY-NC-4.0 / research-only** and disqualified by moedex's "OSS someday"
posture regardless of score. They appear in the table for completeness, flagged.

The honest expectation: **a 2025 code-trained encoder should beat 2021
DistilRoBERTa on the synonym-gap stratum**, but the *magnitude* on our corpus —
especially ColdFusion, which nothing here was trained on — is unproven until the
harness runs. Ship whichever wins the A/B by more than gold-set noise; if none
beats the bundled model meaningfully, keeping it is a defensible outcome.

---

## Options table

Benchmark column is the suite it comes from (CoIR avg NDCG@10 or MTEB-Code avg),
**not** moedex's corpus. "Path" = which moedex integration it fits.

| Model | Params / size | Dim | Max seq | Code-trained? | License | ONNX / HTTP path | Benchmark signal | Notes |
|---|---|---|---|---|---|---|---|---|
| **st-codesearch-distilroberta** (current) | ~82M / int8 ~78MB | 768 | 256 (we cap) | Yes (CodeSearchNet, 2021) | Apache-2.0 | **In-process** (bundled now) | dated; pre-CoIR era | Baseline to beat. Encoder, mean-pool, RoBERTa inputs. |
| **granite-embedding-english-r2** (IBM) | 149M | 768 | 8192 | Yes (incl. SQL) | **Apache-2.0** | **In-process** (export via Optimum) or HTTP | CoIR 55.3 (paper) | **Top pick.** ModernBERT encoder; RoBERTa-style inputs (no token_type_ids); official ONNX (+int8). CLS pooling (mismatch — see below). |
| **granite-embedding-311m-multilingual-r2** (IBM) | 311M | 768 (MRL→512/384/256/128) | 32768 | Yes (incl. SQL) | **Apache-2.0** | **In-process** or HTTP | strong COIR in R2 report | Multilingual fallback if English-only hurts on legacy non-English comments. Encoder, CLS pool. |
| **Qwen3-Embedding-0.6B** (Alibaba) | 0.6B | 1024 (MRL→32) | 32768 | Yes (code in mix) | **Apache-2.0** | **HTTP** (decoder; last-token pool) | MTEB-multi 64.33; #1-class family | Strong + permissive, but last-token pool + left-pad + query instruction → NOT a clean in-process drop-in. Serve it. |
| **Qwen3-Embedding-4B / 8B** | 4B / 8B | up to 2560 / 4096 | 32768 | Yes | **Apache-2.0** | **HTTP** | MTEB-multi #1 (8B 70.58, Jun-2025) | RAM is fine, but huge index dim + decoder pooling. HTTP only. Overkill unless 0.6B underdelivers. |
| **codesage-large-v2** (Amazon) | 1.3B | 2048 (MRL) | (8192-class) | Yes (9 langs, no SQL/CF) | **Apache-2.0** | In-process *if* export is clean | strong CoIR (v2 line) | Encoder, Starcoder tokenizer, **trust_remote_code** (custom modeling — ONNX export risk). 2048-d = 2× index. |
| **codesage-base-v2 / small-v2** (Amazon) | 356M / 130M | 1024 | (8192-class) | Yes (9 langs) | **Apache-2.0** | In-process *if* export clean | base-v2 ~57.5, small-v2 ~54.4 CoIR | Smaller siblings; same trust_remote_code caveat. base-v2 1024-d. |
| **nomic-embed-code** (Nomic) | 7B (Qwen2.5-Coder-7B) | (LLM-sized) | long | Yes (CoRNStack) | **Apache-2.0** | **HTTP** (decoder; last-token) | beats Voyage-3 / OpenAI-3-large on CodeSearchNet | Permissive but a 7B decoder; HTTP only. Heavy. |
| **CodeRankEmbed** (Nomic) | 137M | 768 | — | Yes (CoRNStack) | check card | In-process *maybe* | CoIR ~60.1 | Smaller Nomic code retriever; query prefix required; verify arch/license before A/B. |
| **SFR-Embedding-Code-400M_R** (Salesforce) | 400M | (CLS) | 8192 | Yes (12 langs) | **CC-BY-NC-4.0 / research-only** | n/a | CoIR **61.9** | Strong small encoder, but **non-commercial → disqualified** for OSS moedex. trust_remote_code. |
| **SFR-Embedding-Code-2B_R / 7B** (Salesforce / CodeXEmbed) | 2B / 7B | large | 8192 | Yes (12 langs) | **CC-BY-NC-4.0 / research-only** | n/a | **CoIR #1** (7B), 2B beats Voyage | **Disqualified (non-commercial).** Listed only as the score ceiling. |
| **jina-code-embeddings-0.5b / 1.5b** (Jina) | 0.5B / 1.5B | 896 (MRL→64) | 32768 | Yes (15+ langs) | **CC-BY-NC-4.0** (card) | HTTP only (decoder) | MTEB-Code 0.5B **78.7** (SOTA at size) | Highest at-size code score here, but **CC-BY-NC on the card → disqualified** for OSS. Qwen2.5-Coder base is Apache, but Jina's release license governs. Decoder, last-token. |
| **Qodo-Embed-1-1.5B** (Qodo) | 1.5B | (large) | long | Yes | **OpenRAIL++-M** (permits commercial; non-standard) | HTTP | CoIR **68.53** (1.5B), 70.06 launch | High CoIR, commercial OK, but OpenRAIL is a *behavioral-restriction* license, not OSI-approved — weigh against "clean OSS." 7B is commercial-only. |

---

## CLS vs mean pooling — the one thing to get right in the in-process A/B

`internal/embed/onnx.go` runs the model to `last_hidden_state [batch,seq,dim]`
then calls `meanPool` — attention-masked **mean** over tokens, then L2 norm. This
is correct for the bundled sentence-transformers model and for any model whose
sentence-transformers config is `pooling_mode_mean_tokens`. It is **not** how
several candidates produce their sentence vector:

- **Granite R2** pools **CLS** (`model_output[0][:, 0]`). Mean-pooling its
  `last_hidden_state` is an *approximation*, not the model's intended vector.
  Two options for the A/B: (a) quick — run it through the existing mean-pool path
  and accept the approximation (often still better than 2021 DistilRoBERTa, and
  it isolates "is this model worth a proper integration?"); (b) faithful — add a
  CLS-pool branch to `ONNXEmbedder` (take `hidden[b*seqLen*dim : +dim]` instead
  of meanPool) and gate it by a flag. Recommend running (a) first; if Granite
  even *ties* the baseline under wrong pooling, do (b) and re-measure — the
  faithful pooler should only help.
- **CodeSage / SFR-Code** also use CLS (`last_hidden_state[:, 0]`). Same story.
- **Decoder models (Qwen3, jina-code, nomic)** use **last-token** pooling with
  **left-padding** and a required **query instruction prefix**. moedex's
  right-padded mean-pool would silently produce garbage vectors for these. This
  is the structural reason they belong on the **HTTP path**, where the serving
  runtime (llama.cpp/Ollama/vLLM) does the correct pooling and prompting and
  hands moedex a finished vector via `/embeddings`.

Bottom line: **encoder + CLS/mean → in-process (maybe with a one-line pooler
tweak); decoder + last-token → HTTP.** Don't try to force a decoder through the
in-process mean-pool path.

---

## Shortlist: exact steps to A/B via `TestCorpusCodeModelMeasurement`

The harness (`internal/eval/gold_onnx_test.go`, `TestCorpusCodeModelMeasurement`)
loads an arbitrary HF encoder export from disk via
`embed.NewONNXEmbedderFromFiles(runtimePath, modelPath, tokenizerPath,
[]string{"input_ids","attention_mask"}, dim, 256)` and reports lexical vs
+code-dense vs full-hybrid NDCG/MRR/Recall@5. It hard-codes RoBERTa-style inputs
(`input_ids`, `attention_mask`, **no token_type_ids**) and `maxSeq=256`. It needs:

- `MOEDEX_CODE_MODEL` → path to `model.onnx` (must output **`last_hidden_state`
  [batch,seq,dim]**, not a pre-pooled `sentence_embedding`)
- `MOEDEX_CODE_TOKENIZER` → path to `tokenizer.json` (HF tokenizers format)
- `MOEDEX_CODE_DIM` → hidden size (768 / 1024 / 2048)
- `ONNXRUNTIME_LIB_PATH` → `libonnxruntime.dylib`
- the gold corpus present (`MOEDEX_CORPUS_ROOT` or `~/.moedex-managed`)

> Input-name note: the harness passes `["input_ids","attention_mask"]`. Granite
> (ModernBERT) and the bundled DistilRoBERTa both match this. A **BERT-style**
> model that *requires* `token_type_ids` would need the harness's `inputNames`
> changed to `["input_ids","attention_mask","token_type_ids"]` (the
> `NewONNXEmbedderFromFiles` signature already supports it; only the test's
> literal needs editing). All three shortlist encoders here are RoBERTa-style /
> token_type_ids-optional, so the default works.

### Candidate 1 — granite-embedding-english-r2 (in-process, top pick)

Export an encoder ONNX whose output is `last_hidden_state` (use Optimum; it emits
the raw encoder, and the tokenizer.json comes with the repo):

```bash
pip install "optimum[exporters,onnxruntime]" transformers
# Export the bare encoder (feature-extraction => last_hidden_state output):
optimum-cli export onnx \
  --model ibm-granite/granite-embedding-english-r2 \
  --task feature-extraction \
  granite-r2-onnx/
# Yields granite-r2-onnx/model.onnx (last_hidden_state) + tokenizer.json + config.
# Optional int8 (RAM is not a constraint, but it speeds CPU inference):
optimum-cli onnxruntime quantize --avx512 \
  --onnx_model granite-r2-onnx/ -o granite-r2-onnx-int8/

# Run the A/B:
ONNXRUNTIME_LIB_PATH=/path/to/libonnxruntime.dylib \
MOEDEX_CODE_MODEL=$PWD/granite-r2-onnx/model.onnx \
MOEDEX_CODE_TOKENIZER=$PWD/granite-r2-onnx/tokenizer.json \
MOEDEX_CODE_DIM=768 \
go test -tags onnx ./internal/eval/ -run TestCorpusCodeModelMeasurement -v
```

Pooling note: this runs Granite through moedex's **mean**-pool. If it's even
competitive, add a CLS-pool branch and re-run (see CLS-vs-mean section). A
ready-made community int8 export also exists (`yasserrmd/granite-embedding-r2-onnx`)
— verify its output op is `last_hidden_state` (not a pre-pooled head) before using
it, since Transformers.js-style exports sometimes bake in pooling.

### Candidate 2 — codesage-base-v2 / large-v2 (in-process, stretch)

```bash
optimum-cli export onnx \
  --model codesage/codesage-base-v2 \
  --task feature-extraction --trust-remote-code \
  codesage-base-v2-onnx/
```

`--trust-remote-code` is required (custom modeling). **Verify the export
succeeds and the output is `last_hidden_state`** before trusting it — custom
modeling code is the usual source of broken ONNX exports. Starcoder tokenizer
ships a `tokenizer.json`. CodeSage pools CLS, so the same mean-vs-CLS caveat
applies. Run with `MOEDEX_CODE_DIM=1024` (base-v2) or `2048` (large-v2):

```bash
ONNXRUNTIME_LIB_PATH=/path/to/libonnxruntime.dylib \
MOEDEX_CODE_MODEL=$PWD/codesage-base-v2-onnx/model.onnx \
MOEDEX_CODE_TOKENIZER=$PWD/codesage-base-v2-onnx/tokenizer.json \
MOEDEX_CODE_DIM=1024 \
go test -tags onnx ./internal/eval/ -run TestCorpusCodeModelMeasurement -v
```

### Candidate 3 — Qwen3-Embedding-0.6B (HTTP path, not the ONNX harness)

This is a **decoder** with last-token pooling + a required query instruction
prefix; it does **not** fit `TestCorpusCodeModelMeasurement`'s mean-pool ONNX
path. Serve it and use `embed.NewHTTPEmbedder`:

```bash
# llama.cpp server with last-token pooling (GGUF from Qwen/Qwen3-Embedding-0.6B-GGUF):
llama-server -m qwen3-embedding-0.6b.gguf --embeddings --pooling last -c 4096
# Ollama alternative: `ollama pull qwen3-embedding` then its OpenAI-compatible /v1/embeddings.
```

Then exercise it via `embed.NewHTTPEmbedder(baseURL, "qwen3-embedding-0.6b")`
(POSTs `{"model","input":[...]}` to `{baseURL}/embeddings`, OpenAI-style). To get
an apples-to-apples eval you'd run the existing HTTP-backed eval harness
(`TestCorpusONNXMeasurement` has an HTTP sibling pattern in `internal/eval`)
against this endpoint, with `Dim()` discovered as 1024. **Important for Qwen3:**
queries need the instruction prefix `Instruct: {task}\nQuery:{q}` while documents
do not — the HTTP server must apply this, or recall will be depressed. This
asymmetry is exactly the kind of thing the bundled in-process model avoids, and a
reason to prefer an encoder if the encoder is "good enough."

---

## Honesty section

- **Benchmark transfer is weak.** CoIR (CodeSearchNet, CosQA, text↔code,
  code↔code) and MTEB-Code measure curated open-source retrieval, mostly
  Python/JS/Java. moedex's corpus is C#/.NET, Angular TypeScript, MySQL SQL, and
  **legacy ColdFusion**. A model's CoIR rank tells you it learned code semantics;
  it does **not** tell you it generalizes to a .NET/CFML monorepo. Granite's SQL
  coverage and Qwen3's breadth are encouraging *priors*, not evidence.
- **ColdFusion is a true blind spot.** No public code-retrieval benchmark covers
  ColdFusion (CFML), and none of these models advertise CFML training. The
  in-process tokenizer already had to be hardened against Windows-1252 CFML bytes
  (`toTokenizerSafeText`, the `encodeRecover` panic guard). Any new model inherits
  that fragility, and its ColdFusion recall is genuinely unknown until measured.
  The synonym-gap gold (the 6 agent-NL queries) is the closest proxy we have, and
  it's tiny.
- **int8 quantization is a real but usually small tradeoff.** The bundled model
  was int8 with "no regression vs fp32" on our gold (per `onnx.go`'s header), and
  Granite ships int8 ONNX. But quantization error interacts with cosine ranking;
  if a quantized candidate ties fp32 baseline, re-run the A/B in fp32 before
  concluding the model is no better — you may be measuring quantization noise, not
  the model.
- **Dimension changes ripple.** Moving 768→1024 (Qwen3, CodeSage-base) or
  768→2048 (CodeSage-large) grows the flat `embed.Store` and the brute-force
  cosine cost proportionally. RAM is not the constraint, but query latency on the
  full corpus is — note it when comparing a 2048-d model that "wins" by a hair.
- **The no-labeled-data ceiling still binds (same as the reranker doc).** We can
  only *prove* a win on a externally configured answerable and synonym-gap
  queries. That is enough to reject a model that regresses and to rank-order two
  candidates, but **not** enough to certify a published quality claim. A model can
  look better on a small NL stratum by luck. Mitigations: report the agent-NL split
  separately (the harness already does), require the margin to exceed the
  per-query swing the gate documents (~`denseSlack` 0.04), and treat the result as
  "promote to default, keep watching" rather than "settled."
- **Pooling mismatch can mask a good model.** As above, running a CLS/last-token
  model through the mean-pool path under-sells it. A null result from the
  in-process harness on Granite/CodeSage is **not** conclusive until you've tried
  the faithful pooler. Conversely, an HTTP-served Qwen3 with the *wrong* (or
  missing) query instruction prefix will also under-sell it.

---

## Sources (URL — claim — date)

- [CoIR: A Comprehensive Benchmark for Code Information Retrieval (arXiv 2407.02883)](https://arxiv.org/pdf/2407.02883) — defines CoIR (10 datasets / 7 domains); notes CodeSearchNet overfitting; the standard code-retrieval suite numbers below are measured on it. 2024.
- [Granite Embedding R2 Models (arXiv 2508.21085)](https://arxiv.org/pdf/2508.21085) — ModernBERT-based bi-encoders; trained on web + code (Python/Go/Java/JS/PHP/Ruby/SQL/C/C++); Apache-2.0; English 149M/768-d, multilingual 97M/311M; CoIR among reported benchmarks. 2025.
- [Granite Embedding Multilingual R2 (HF blog, ibm-granite)](https://huggingface.co/blog/ibm-granite/granite-embedding-multilingual-r2) — Apache-2.0, 32K context, pre-converted ONNX + OpenVINO weights, Optimum-loadable; sub-100M retrieval quality claim. 2026.
- [ibm-granite/granite-embedding-english-r2 (HF model card)](https://huggingface.co/ibm-granite/granite-embedding-english-r2) — 149M, 768-d, 8192 max seq, Apache-2.0, ModernBERT; usage shows CLS pooling (`model_output[0][:,0]`); CoIR 55.3. Accessed 2026-06-24.
- [ModernBERT (HF docs / answerdotai model card)](https://huggingface.co/docs/transformers/en/model_doc/modernbert) — "does not use token type IDs, unlike some earlier BERT models"; RoPE + local-global attention; encoder, 8192 ctx. Confirms RoBERTa-style inputs for Granite R2. Accessed 2026-06-24.
- [onnx-community/granite-embedding-small-english-r2-ONNX (HF)](https://huggingface.co/onnx-community/granite-embedding-small-english-r2-ONNX) — official-community ONNX export of the small (47M/384-d) R2; q8 (int8) dtype available. Accessed 2026-06-24.
- [yasserrmd/granite-embedding-r2-onnx (HF)](https://huggingface.co/yasserrmd/granite-embedding-r2-onnx) — community INT8 ONNX of full English R2 via Optimum/ORT (verify output is last_hidden_state, not pre-pooled). Accessed 2026-06-24.
- [Qwen3 Embedding (arXiv 2506.05176 / qwenlm.github.io blog)](https://qwenlm.github.io/blog/qwen3-embedding/) — 0.6B/4B/8B; Apache-2.0; MTEB-multi #1 (8B 70.58, Jun-5-2025); code retrieval among tasks; MRL dims; instruction-aware. Jun 2025.
- [Qwen/Qwen3-Embedding-0.6B (HF model card)](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) — 1024-d (MRL→32), 32K ctx, **last-token pooling with left-padding**, required query instruction `Instruct:{task}\nQuery:{q}`, Apache-2.0. Decoder ⇒ HTTP path. Accessed 2026-06-24.
- [zhiqing/Qwen3-Embedding-0.6B-ONNX / -4B-ONNX (HF)](https://huggingface.co/zhiqing/Qwen3-Embedding-0.6B-ONNX) — community ONNX exports exist, but the last-token/left-pad/instruction contract makes the in-process mean-pool path wrong; prefer a serving runtime. Accessed 2026-06-24.
- [codesage/codesage-large-v2 (HF) + CodeSage-v2 site](https://huggingface.co/codesage/codesage-large-v2) — Amazon encoder, Apache-2.0, Starcoder tokenizer, Matryoshka; small/base/large = 130M/356M/1.3B at 1024/1024/2048-d; 9 langs (no SQL/CF); **trust_remote_code=True**; CLS pooling. Accessed 2026-06-24.
- [CodeXEmbed / SFR-Embedding-Code (arXiv 2411.12644; Salesforce blog; HF 400M_R/2B_R)](https://huggingface.co/Salesforce/SFR-Embedding-Code-400M_R) — 400M/2B/7B, 12 langs, 8192 ctx, CLS pooling, trust_remote_code; **CC-BY-NC-4.0, research-only**; 7B #1 on CoIR, 400M CoIR 61.9. Disqualified (non-commercial). Accessed 2026-06-24.
- [jina-code-embeddings (arXiv 2508.21290; jinaai HF 0.5b/1.5b)](https://huggingface.co/jinaai/jina-code-embeddings-0.5b) — Qwen2.5-Coder backbone, 896-d (MRL→64), 32K ctx, **last-token pooling**, instruction prefixes; MTEB-Code 0.5B 78.7 (SOTA at size); card license **cc-by-nc-4.0**. Disqualified (non-commercial) + decoder. EMNLP 2025 / Aug 2025.
- [Qodo-Embed-1 (qodo.ai blog; HF Qodo/Qodo-Embed-1-1.5B)](https://www.qodo.ai/blog/qodo-embed-1-code-embedding-code-retrieval/) — 1.5B, CoIR 68.53 (launch 70.06), **OpenRAIL++-M** (commercial allowed, non-OSI behavioral license); 7B commercial-only. Feb-May 2025.
- [Nomic Embed Code (nomic-ai/nomic-embed-code HF; Simon Willison writeup; CoRNStack arXiv 2412.01007)](https://huggingface.co/nomic-ai/nomic-embed-code) — Qwen2.5-Coder-7B, **Apache-2.0**, last-token pooling, query prefix required; beats Voyage-3/OpenAI-3-large on CodeSearchNet; 7B decoder ⇒ HTTP. Mar 2025.
- [internal/embed/onnx.go (in-repo)](../internal/embed/onnx.go) — `NewONNXEmbedderFromFiles(runtimePath, modelPath, tokenizerPath, inputNames, dim, maxSeq)`; output must be `last_hidden_state [batch,seq,dim]`; mean-pool + L2 norm; UTF-8/CFML tokenizer hardening. Verified 2026-06-24.
- [internal/embed/embed.go (in-repo)](../internal/embed/embed.go) — `Embedder` interface; `NewHTTPEmbedder(baseURL, model)` POSTs OpenAI-style `/embeddings`; flat brute-force cosine `Store`. Verified 2026-06-24.
- [internal/eval/gold_onnx_test.go (in-repo)](../internal/eval/gold_onnx_test.go) — `TestCorpusCodeModelMeasurement` loads disk export via env `MOEDEX_CODE_MODEL`/`_TOKENIZER`/`_DIM` + `ONNXRUNTIME_LIB_PATH`, inputs `["input_ids","attention_mask"]`, maxSeq 256, dim default 768; reports lexical vs +code-dense vs full-hybrid. Verified 2026-06-24.
- [research/learned-reranker.md (in-repo)](./learned-reranker.md) — companion doc; dense arm is gated/complementary, no-labels ceiling, external gold and agent-NL split; structure mirrored here. 2026-06-22.
