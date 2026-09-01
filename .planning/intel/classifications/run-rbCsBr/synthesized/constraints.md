# Staged Constraints

(None extracted.)

No document in this ingest set was classified `SPEC`. The 25 classifications break down as
22 ADR, 2 DOC, 1 PRD — zero SPEC — so this file has no source material under the extraction
contract (SPECs feed constraints.md).

This is an absence, not an inference. Binding numeric and behavioral constraints do exist in
this corpus, but they are stated inside ADR decision statements and are staged there rather
than being re-typed here as SPEC constraints. Downstream consumers should read them from
`decisions.md`; the load-bearing ones are:

- never under-approximate the trigram reduction; ripgrep is immovable ground truth (ADR-0003)
- ~8 GB corpus envelope, single node, ~8-16 GB RAM; build (not serve) is the memory-binding step (ADR-0001)
- retrieval core adds no required dependency of its own; only the MCP surface takes a required external dep (ADR-0001 as amended 2026-08-25, ADR-0022)
- production MeanNDCG >= 0.85 and lexical-only >= 0.58 as a hard CI gate (ADR-0014)
- `DefaultTokenBudget = 8000`; the running estimate never exceeds the budget (ADR-0009)
- `RRFk = 60`, BM25 `K1 = 1.2` / `B = 0.75`, `PathMinCoverage = 0.6`, `SymbolMinCoverage = 0.67`, `DenseMinQueryTerms = 5` (ADR-0006)
- byte offsets, not rune offsets, throughout the core; LSP `positionEncoding` negotiated to `utf-8` to stay aligned (ADR-0002, ADR-0017)
- MCP protocol `2026-07-28` current, initialize-era negotiation capped at `2025-11-25`, JSON-RPC batches rejected in both eras (ADR-0022)
- fail-closed AI-privacy: a policy read or parse error is fatal; level-1 paths are excluded before the file is opened; a failed build publishes nothing (ADR-0021)
