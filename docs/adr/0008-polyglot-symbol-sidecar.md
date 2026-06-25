# ADR 0008: Polyglot syntactic symbol layer as a precomputed sidecar — not tree-sitter in the binary

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
Two things needed real symbol boundaries: context assembly ([0009](./0009-agent-context-api.md)) was scoping blocks with a brace/indent heuristic that the package itself called "honestly approximate," and the ranker ([0006](./0006-rrf-hybrid-ranking.md)) wanted a symbol-name arm. The research note ([`research/symbol-layer.md`](../../research/symbol-layer.md)) split the conflated question: *enclosing-symbol scoping* (function/class ranges, syntactic, no cross-file resolution) is separate from *go-to-def/find-references* (cross-file, typechecker-grade, can lag). It recommended a precomputed, ingest-only symbol model that keeps the engine pure-Go — running any heavy parser out-of-process at index time rather than linking it into the binary.

## Decision
Build a **polyglot syntactic symbol layer** (`internal/symbol`) that emits a **precomputed sidecar** (`SYM1` codec), not an in-binary tree-sitter/SCIP parser.

- `BuildMulti` dispatches a language-appropriate `Extractor` per blob via `ExtractorForPath` (keyed on extension): Go (`go/parser`, the one real parser, already in stdlib), C#, TypeScript, SQL, and ColdFusion (the non-Go ones are regex/byte scanners).
- Symbols carry name, kind, and four byte offsets (name + body start/end). The sidecar persists them for the whole corpus.
- It feeds two consumers: block scoping (`Enclosing`/`EnclosingBytesFunc`, replacing the heuristic where a symbol is found, falling back to it on a miss) and the symbol-name ranking arm (`SetSymbols`).

## Consequences
**Positive**
- No cgo, no tree-sitter grammars, no parser linked into the binary — the pure-Go posture ([0001](./0001-single-node-scope-pure-go-default.md)) holds; only the Go stdlib `go/parser` is used.
- A precomputed sidecar means no per-query parsing cost and no staleness within a build; it follows the same optional-sidecar boundary as the dense arm ([0007](./0007-optional-dense-arm.md)).
- Context blocks land on real definition boundaries; the symbol-name arm gives definitions their due weight.

**Negative / costs**
- The non-Go extractors are **syntactic and best-effort** (regex/byte scanners, not full parsers) — they miss what a real grammar would catch; a blob that yields no symbols falls back to the brace/indent heuristic.
- **No go-to-def / find-references** — this layer scopes and ranks; a cross-file reference graph (SCIP-grade) is deliberately deferred future work.
- Deeper/semantic symbols (tree-sitter `tags.scm`, more languages) remain on the table per the research note, not built.

## Evidence
The symbol arm fires across the polyglot corpus (Go/C#/TS/SQL/CFML), each extractor with tests; the gold gate fails if the symbol arm attaches to zero blobs. Concrete wins from adding extractors: the ColdFusion extractor moved the "void transaction" gold query NDCG **0.689 → 0.964**; the symbol and path arms together measured complementary (production NDCG 0.93 ≫ either alone, [0006](./0006-rrf-hybrid-ranking.md)).

## Related
[0001](./0001-single-node-scope-pure-go-default.md), [0006](./0006-rrf-hybrid-ranking.md), [0009](./0009-agent-context-api.md).
