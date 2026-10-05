# ADR 0009: Agent-first context API — token-budgeted, deduplicated, symbol-scoped windows over MCP

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex

## Context
The research pass named the **absence of a first-class agent/RAG context API the biggest gap in Zoekt's original design.** The implicit assumption there is "a human reads the results"; moedex's primary consumer is an LLM agent. Evidence: with good oracle context GPT-4o gained +27.4% on SWE-Bench, but real systems are bottlenecked on *both* ends — retrievers fail to fetch useful context and generators fail to use raw hits. Handing an agent a flat list of grep lines wastes its context window on duplication and half-functions.

## Decision
Expose the index as an **agent tool**, not a grep server: return **ranked, deduplicated, symbol-scoped, token-budgeted context blocks** over **MCP** (JSON-RPC/stdio), via the `search_context` tool.

- `internal/contextwin.Assemble` takes the ranked results ([0006](./0006-rrf-hybrid-ranking.md)), expands each salient `LineSpan` to its **enclosing block** (`symbol.Index.Enclosing` when wired — [0008](./0008-polyglot-symbol-sidecar.md) — else the brace/indent heuristic), merges blocks separated by at most one blank line per file, and demotes import/header-dominated blocks before its deterministic budget walk. An overflowing first block is clipped around its highest-ranked salient line, including a UTF-8-safe prefix when one line alone exceeds capacity. `Clipped` means returned source was narrowed; `Truncated` means lower-ranked candidates were omitted. The running estimate (`ceil(len/4)`) never exceeds the budget (`DefaultTokenBudget = 8000`).
- `internal/mcp` serves `search_context` (args: `query` required, `token_budget`/`top_k` optional), rendering each block under a `path:start-end (score)` header. Single-repo via `cmd/moedex-mcp`; whole-corpus via `moedex-serve -mcp` ([0010](./0010-warm-serving-spine.md)). The official SDK transport, dual-era protocol negotiation, typed output, and snapshot identity contract are specified in [0022](./0022-mcp-sdk-contract-and-snapshot-identity.md).

## Consequences
**Positive**
- The agent receives whole, scoped units (functions/classes) under a hard token budget instead of raw lines — directly targeting the "generator fails to use context" half of the bottleneck.
- Deterministic ordering and dedup make the tool's output reproducible and diff-able.
- MCP-over-stdio inherits the parent process's trust, so the agent surface needs no separate auth (only the `-http` retrieval surface does — [0010](./0010-warm-serving-spine.md)).

**Negative / costs**
- The token budget is a `len/4` estimate, not a real tokenizer count — close enough for budgeting, not exact.
- Block quality is bounded by the symbol layer's coverage ([0008](./0008-polyglot-symbol-sidecar.md)); on a miss the heuristic can over- or under-scope a block.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related
[0006](./0006-rrf-hybrid-ranking.md), [0008](./0008-polyglot-symbol-sidecar.md), [0010](./0010-warm-serving-spine.md), [0014](./0014-eval-harness-gold-gate.md), [0022](./0022-mcp-sdk-contract-and-snapshot-identity.md).
