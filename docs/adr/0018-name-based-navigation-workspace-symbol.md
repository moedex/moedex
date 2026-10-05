# ADR 0018: Name-based navigation — `find_symbol` (workspace/symbol) + `symbols_overview` (documentSymbol)

- **Status:** Proposed (2026-07-01; implemented same day: `Symbol` type, `LSP`/`Pool`
  `WorkspaceSymbol`/`DocumentSymbol`, and the `find_symbol`/`symbols_overview` MCP tools in
  `internal/navigate` + `cmd/moedex-serve/nav_lsp.go`, all TDD'd against a real gopls). The unblock
  for a Protostar Serena-replacement; the position tools
  ([0017](./0017-lsp-navigation-and-the-serena-boundary.md)) are the other half.
- **Date:** 2026-07-01
- **Context owner:** moedex

## Context

[0017](./0017-lsp-navigation-and-the-serena-boundary.md) shipped three **position-based** nav MCP
tools — `find_definition` / `find_references` / `find_implementations` — each taking `file:line:col`
and returning bare `file:line:col` locations. That is the raw LSP model.

Protostar consumes navigation through a **name-based** interface (its `CodebaseMapper`:
`findSymbol(name)` / `findReferencingSymbols(name)` / `findImplementations(name)` /
`findDeclaration(name)` / `getSymbolsOverview(path)`, driven by `queryIntel`). Serena satisfies that
interface because it resolves a **name → position** internally before running the LSP query. moedex
does not: it exposes only the position tools, so a caller holding a *name* (the normal case) cannot
use them. Building the Protostar adapter surfaced this as the **third adoption gate** (Protostar ADR
0002, 2026-07-01 amendment) — and it lives here, not in Protostar.

The mismatch is **bidirectional**:
- **Input:** name → position resolution is missing.
- **Output:** the position tools return *bare locations with no symbol name/kind*; the name-based
  interface returns *named symbols* (`SymbolRef{name, kind, path, line}`).

Both halves are closed by the two standard LSP methods the pooled servers already speak but moedex
does not yet surface: `workspace/symbol` and `textDocument/documentSymbol`.

## Decision

Add two navigation tools (and their `navigate` client + `Pool` methods), returning a **named** symbol
result — not the bare `Location` the position tools return.

### New result type

A `Symbol { Name string; Kind string; Loc Location }` (LSP `SymbolInformation`/`DocumentSymbol` →
name + kind + `file:line:col`). The position tools keep returning `Location` unchanged; only the
name-based tools carry name+kind (that is the output half of the fix).

### Tool 1 — `find_symbol` (LSP `workspace/symbol`)

- **Input:** `{ query: string, root: string, lang?: string }`. `query` is the symbol name (LSP
  `workspace/symbol` is a fuzzy/substring match — document that it is not exact-only).
- **Output:** `Symbol[]` as text, one `name\tkind\tfile:line:col` per line (or reuse the existing
  `file:line:col` line format with a leading `name kind` — pick one and pin it in a test; Protostar's
  `moedex-nav` parser will mirror whatever is chosen).
- **Server routing (the non-obvious part):** `workspace/symbol` is **workspace-scoped**, not
  file-scoped, so it cannot be routed by a file's extension like the position tools. Route by `root`:
  select the `(root, lang)` server. In a polyglot root where `lang` is omitted, query **each live
  language server for that root and merge** (a symbol name can exist in more than one language). Note
  the coverage honesty: only languages whose server is up contribute.

### Tool 2 — `symbols_overview` (LSP `textDocument/documentSymbol`)

- **Input:** `{ file: string }` — file-scoped, so it routes exactly like the position tools (by
  extension + enclosing root).
- **Output:** `Symbol[]` for that file (top-level + nested declarations, flattened).

### Client / Pool

- `LSP.WorkspaceSymbol(ctx, query)` and `LSP.DocumentSymbol(ctx, file)` on top of the existing
  generic `call(ctx, method, params)` primitive (`internal/navigate/lsp.go`) — the same way
  `locationQuery` wraps `call` for the position verbs. Reuse the negotiated **utf-8 `positionEncoding`**
  so columns stay byte-columns aligned with the core ([0002](./0002-positional-trigram-core-byte-offsets.md)),
  exactly as the position tools do.
- `Pool.WorkspaceSymbol(ctx, root, query)` (root-routed, merge-on-polyglot) and
  `Pool.DocumentSymbol(ctx, file)` (file-routed), reusing the shared server lifecycle.
- **Capability honesty (mirror 0017):** a server without a `workspace/symbol` or `documentSymbol`
  provider returns JSON-RPC `-32601`; map it to **empty results, not an error**, so a language
  without name lookup degrades cleanly rather than throwing.

## Consequences

**Positive**
- **Closes the third gate in Protostar ADR 0002.** With name→position resolution available, the
  already-built Protostar position-native core (`packages/capabilities/src/moedex-nav.ts`, 2026-07-01)
  composes into a full name-based `CodebaseMapper` — a real Serena drop-in:
  - `findSymbol(name)` / `findDeclaration(name)` → `find_symbol` directly (named results).
  - `getSymbolsOverview(path)` → `symbols_overview` directly.
  - `findReferencingSymbols(name)` / `findImplementations(name)` → `find_symbol(name)` to get a
    position, then the existing `find_references` / `find_implementations` at that position (two-step,
    exactly what Serena does internally).
- Fixes **both** halves of the mismatch: input (name→position) and output (bare location→named symbol).

**Negative / costs**
- `workspace/symbol` quality varies by server (fuzzy match, ranking, completeness differ across gopls
  / csharp-ls / typescript-language-server); it is a *search*, not an exact resolver — the two-step
  reference path inherits that seed imprecision. Acceptable: the same class of imprecision Serena has.
- The polyglot-root merge means one `find_symbol` call may fan out to several servers; bounded by the
  live-server set the Pool already caps.
- `searchForPattern` (Serena's sixth method) is **out of scope** here — it is text search, not LSP
  navigation; it stays with Serena / the Protostar bundled `static-scan` mapper / `search_context`.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related

[0008](./0008-polyglot-symbol-sidecar.md) (the *syntactic* symbol sidecar — distinct from this
*type-resolved* LSP symbol lookup), [0017](./0017-lsp-navigation-and-the-serena-boundary.md) (the
position tools this completes); Protostar ADR 0002 (the context seam).
