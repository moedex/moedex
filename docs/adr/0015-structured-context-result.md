# ADR 0015: Structured `search_context` result — lossless blocks + per-arm provenance for machine consumers

- **Status:** Accepted
- **Date:** 2026-06-26
- **Context owner:** moedex (TurnCommerce)

## Context
[0009](./0009-agent-context-api.md) made `search_context` return human-readable
text: `formatWindow` renders a summary line plus each block under a
`path:start-end (score)` header (`internal/mcp/mcp.go:428-443`). That is the
right surface when *an agent reads the string directly*.

A new consumer breaks that assumption. **Moe** (a separate context-fusion layer,
`~/Code/moe`) consumes moedex *as one of several MCP backends*, normalizes each
backend's output into typed evidence, and fuses across sources with per-source
confidence. For Moe, the current text output is lossy at the very first hop:

- Block fields must be **regex-parsed** back out of the `--- path:start-end
  (score X) ---` header.
- Only the **fused** `Score` is in the text. `RankedResult.Lexical` (BM25) and
  `RankedResult.Dense` (cosine) exist on the struct (`internal/rank/rank.go:32-39`)
  but are dropped at the `contextwin` boundary — `ContextBlock` carries only
  `Score` (`internal/contextwin/contextwin.go:43-58`).
- Content identity (`Blob`) — the natural dedup key across calls and sources —
  is not exposed at all.

Moe's confidence ceiling is set by how much structured score + provenance each
backend exposes. moedex is a hop we control, so it should expose the most.

## Decision
Add an **opt-in structured result mode** to `search_context`. Default behavior is
unchanged (text), preserving every existing consumer and [0009](./0009-agent-context-api.md)'s
contract byte-for-byte.

**2026-08-24 amendment:** [0022](./0022-mcp-sdk-contract-and-snapshot-identity.md)
supersedes the opt-in portion of this decision. `structuredContent` is now always
present and validates against the advertised output schema; `format` controls only
the text fallback. Blocks also carry the canonical indexed `blob_sha`, while the
numeric `blob` remains a process/snapshot-local provenance field.

- New optional arg `format` on the tool: `"text"` (default) | `"structured"`.
  Add it to `toolDescriptor` and `callParams.Arguments` (`internal/mcp/mcp.go:368-391`).
- When `format == "structured"`, `callTool` returns the window as a JSON object in
  the result's `structuredContent` field (MCP's typed-result channel), with a
  short text line in `content` as the required human-readable fallback. When
  `format == "text"` (or absent), the path is exactly today's `formatWindow`.
- Structured payload (new JSON DTO in `internal/mcp`, stdlib `encoding/json` only —
  keeps the pure-Go, zero-dep invariant):

  ```json
  {
    "summary": { "blocks": 3, "token_estimate": 740, "truncated": false, "clipped": false },
    "blocks": [
      {
        "blob": 12407715319,
        "repo": "...", "rel_path": "internal/auth/refresh.go", "abs_path": "...",
        "start_line": 88, "end_line": 121,
        "score": 0.0312, "lexical": 7.41, "dense": 0.83, "clipped": false,
        "text": "func (s *Session) Refresh(...) ..."
      }
    ]
  }
  ```

  `clipped` means returned source was narrowed around its salient line to honor
  the hard token budget. `truncated` remains reserved for omitted lower-ranked
  candidates; both can be true independently.

- To make `lexical`/`dense`/`blob` available at serialization, thread them onto
  the assembled block. `contextwin.Assemble` already copies `Score` from the
  source `RankedResult` onto each `ContextBlock`; extend that same inheritance to
  carry `Lexical`, `Dense`, and `Blob` (`internal/contextwin/contextwin.go`). One
  source of truth; no second pass zipping blocks back to results.

### Deliberately deferred
Full per-arm provenance — `SymbolCoverage`, `PathCoverage`, and the per-arm ranks
(`LexRank`/`DenseRank`/`SymRank`/`PathRank` in the internal `FeatureVector`) — is
**not** exposed here. Those are dropped before `contextwin` and surfacing them is
a larger change to the ranker's output contract. v1 exposes the fused `Score`
plus the two raw arm scores already kept on `RankedResult`. Revisit if Moe's
fusion needs per-arm agreement signal from moedex specifically.

## Consequences
**Positive**
- Machine consumers (Moe) read typed blocks with stable fields, content identity
  (`blob`) for cross-call/cross-source dedup, and the per-arm scores that drive
  confidence — no regex parsing, no provenance loss at hop one.
- Default text path is untouched: [0009](./0009-agent-context-api.md) consumers
  and `moedex-mcp` / `moedex-serve -mcp` see no behavior change.
- Pure-Go invariant intact (`encoding/json` is stdlib); no `internal/query`,
  `internal/search`, or `internal/parity` change, so `make verify` stays green.

**Negative / costs**
- `ContextBlock` gains three provenance fields, coupling `contextwin` a little
  more tightly to ranking internals (it already carries `Score`, so the seam
  exists).
- A small new code path and DTO to keep in sync with `ContextBlock`.
- `dense` is 0 when the dense arm did not fire (gated by `DenseMinQueryTerms`);
  consumers must treat 0 as "arm absent," not "cosine 0."

## Evidence
Current flattening: `callTool` → `formatWindow` returns a single text blob
(`internal/mcp/mcp.go:414, 428-443`). Dropped provenance: `RankedResult` holds
`Lexical`/`Dense` (`internal/rank/rank.go:32-39`) but `ContextBlock` keeps only
`Score` (`internal/contextwin/contextwin.go:43-58`). MCP already lets a tool
result carry both `content` and `structuredContent`, so the addition is
backward-compatible by construction.

## Related
[0009](./0009-agent-context-api.md) (extends its output contract),
[0006](./0006-rrf-hybrid-ranking.md) (source of the arm scores),
[0010](./0010-warm-serving-spine.md) (the other serving path that must mirror the arg).
Consumed by Moe (`~/Code/moe/docs/adr/0001-scope-fusion-model-and-stack.md`).
