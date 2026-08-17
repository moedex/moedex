# Symbol Layer for moedex — Implementation-Ready Guidance

> Research date: 2026-06-22. Builds on the initial architecture research, which
> lists a "Symbol/semantic layer" as an addition but flags it as **synthesis, no surviving
> verified claims — an open question**. This doc resolves that open question
> against current (2026) sources and moedex's actual code. Evidence is tagged
> **[confirmed]** (primary source) vs **[synthesis]** (engineering judgment).

---

## Summary / Recommendation

**Adopt a precomputed-symbol model, but split the open question in two — because
the doc conflated two different things under "symbol layer":**

1. **Enclosing-symbol scoping** (what `contextwin` actually needs today: replace
   the brace/indent heuristic with real function/class boundaries). This needs
   only *syntactic* symbol extraction — definition ranges, no cross-file
   resolution. The right tool is **tree-sitter `tags.scm` queries**, the same
   mechanism GitHub code-nav and aider use. **[confirmed]**

2. **Go-to-def / find-references** (resolved cross-file symbol graph). This is
   genuinely hard, language-specific, and needs a typechecker. The right tool is
   **precomputed SCIP** (ingest `scip-go`/`scip-typescript` output), *not*
   on-the-fly parsing — you cannot do reliable find-refs from a single-file parse.
   **[confirmed]**

**So the doc's open question — "is precomputed symbol indexing better than
on-the-fly tree-sitter?" — resolves to: they solve different problems, not the
same one at different times.** On-the-fly tree-sitter gives you *syntactic*
symbols (cheap, multi-lang, never stale, no resolution). Precomputed SCIP gives
you *semantic* symbols (expensive, per-language indexer, can go stale, full
resolution). The 2026 answer is **use tree-sitter tags for scoping + ranking now,
ingest SCIP for navigation later** — they layer, they don't compete.

**Concrete recommendation for moedex's first slice:** a pure-data, ingest-only
symbol layer.

- **Preserve the pure-stdlib property.** Do *not* link a parser into the moedex
  binary. Run tree-sitter (`tree-sitter tags`, CLI subprocess) or `scip-*`
  indexers **out-of-process at index time**, emit a compact sidecar, and have
  moedex ingest only **pure data** (no cgo, no parser dependency, fully
  `go get`-able). This mirrors how the existing `embed` package treats the
  embedding server as an external process and keeps the dense arm optional. The
  Waldo predecessor already does exactly this with SCIP sidecars (per the
  `waldo-cluster-reindex` playbook), so the pattern is proven in-house.
- **First increment:** symbolic *enclosing-range* index keyed by blob SHA → a
  function/class boundary that `contextwin.expandSpan` consults before falling
  back to the brace heuristic. One language (Go), syntactic only, no refs.

This is the smallest valuable increment, keeps every current constraint intact,
and is reversible.

---

## Options table

| Dimension | On-the-fly tree-sitter (parse at index time) | tree-sitter `tags.scm` (syntactic tags) | Precomputed SCIP (ingest indexer output) |
|---|---|---|---|
| **What it gives you** | Full parse tree → any syntactic fact | Definition/reference *tags* with names + ranges | Resolved symbols: go-to-def, find-refs, implementations |
| **go-to-def / find-refs quality** | Single-file only — no cross-file resolution | Single-file; refs are *unresolved* name mentions | High — built on real typecheckers/compilers **[confirmed]** |
| **Multi-language coverage** | 200+ grammars; uniform API **[confirmed]** | 200+ grammars ship `tags.scm`; consumable as-is **[confirmed]** | Per-language indexer; ~10 mature (go, ts, java, py, rust, c/c++, ruby, dotnet, php, dart) **[confirmed]** |
| **Build/runtime cost** | Parse every file at index time (fast: full parse ~1-2ms/file) | Same parse cost + query eval (cheap) | Heavy: must *build/typecheck* the repo (needs `go.mod`, toolchain) **[confirmed]** |
| **Staleness** | None — derived from current content | None — derived from current content | Can lag: index is a separate artifact from content **[confirmed]** |
| **Storage** | N/A (transient) → you persist what you extract | Tiny: name + kind + byte range per symbol | Larger; SCIP ~4-5x smaller than LSIF but still a full graph **[confirmed]** |
| **moedex purity cost** | cgo (official binding) OR pure-Go reimpl OR subprocess | Same — but `tree-sitter tags` CLI subprocess = zero in-binary dep | **Zero** in-binary dep if you ingest precomputed `.scip` protobuf **[confirmed]** |
| **Best for** | Rich per-file analysis you control | **Enclosing-scope + symbol-name ranking (moedex's real need)** | Navigation features (a later slice) |

**Key resolution of the doc's open question:** find-refs *cannot* be done
on-the-fly from a single-file tree-sitter parse — references resolve across files
and need type information. So "on-the-fly tree-sitter" is not a substitute for
SCIP for navigation; it's a substitute for SCIP *only* for syntactic scoping and
name-based ranking. **[confirmed]** For the scoping/ranking job moedex needs
first, on-the-fly tree-sitter (via tags) is strictly better: no staleness, no
build step, multi-language for near-free. For navigation, precomputed SCIP wins.

---

## Go-integration specifics (what breaks purity, what doesn't)

### tree-sitter from Go (2026 landscape) **[confirmed]**
- **Official: `tree-sitter/go-tree-sitter`** — maintained, but **cgo**. Grammars
  are separate `go get` modules; you must call `Close()` on Parser/Tree/Query/etc.
  because `runtime.SetFinalizer` + cgo is buggy. cgo breaks `CGO_ENABLED=0`
  cross-compilation and forces a C toolchain into CI — a direct hit to moedex's
  pure-stdlib, easy-build, OSS-someday properties.
- **Legacy: `smacker/go-tree-sitter`** — cgo, bundles grammars. Same purity cost.
- **CGO-free, emerging in 2026:**
  - `odvcencio/gotreesitter` — **pure-Go reimplementation** of the tree-sitter
    runtime; reads the same parse-table format (existing grammars work without
    recompilation); reports ~1.5x faster full parse and dramatically faster
    incremental reparse than cgo. *Pure Go = preserves moedex's purity if it
    holds up.* **[synthesis on maturity — verify before depending on it]**
  - `malivvan/tree-sitter` — wraps a Wasm tree-sitter build via `wazero`
    (pure-Go Wasm runtime). cgo-free but explicitly **pre-release**.
- **Cleanest purity-preserving path: don't link any of them.** Run the
  `tree-sitter` CLI as an out-of-process step (`tree-sitter tags <files>`), which
  emits name/role/kind/range per entity, and have moedex ingest that text/JSON.
  The parser dependency lives in the *indexer toolchain*, never in the moedex
  binary — exactly the boundary the `embed` package already draws for the
  embedding server.

### SCIP from Go **[confirmed]**
- SCIP is a **protobuf schema** (`scip.proto`): `Index → Document → Occurrence`,
  plus `Symbol` / `SymbolInformation`. It is **pure data — no parser needed to
  consume it.** Consuming a `.scip` file is just protobuf deserialization.
- `Occurrence` carries a range (`SingleLineRange{line,start_char,end_char}` or
  `MultiLineRange`), **0-based**, with a `PositionEncoding` (UTF-8 bytes /
  UTF-16 / UTF-32). `SymbolRole` is a bitset: `Definition=0x1`, reference is the
  implicit default, plus Import/Read/Write/Generated/Test.
- `Symbol` is a structured URI string: `<scheme> <package> (<descriptor>)+` with
  suffixes `/`=namespace `#`=type `.`=term `()`=method — globally meaningful, so
  the same symbol string links defs and refs across files/repos.
- Go bindings exist: `github.com/sourcegraph/scip` (the `scip` Go module + CLI).
  But to stay pure-stdlib you can either vendor the generated `.pb.go` (it's
  stdlib `encoding`-style protobuf — no cgo) **or** convert SCIP to your own
  compact format in the out-of-process indexer and ingest *that*. Either way the
  protobuf/protoc toolchain need not be a moedex build dependency.
- **Indexers** (run out-of-process, like Waldo already does): `scip-go` (now
  `github.com/scip-code/scip-go`, v0.2.x, May 2025, needs `go.mod`),
  `scip-typescript`, `scip-java`, `scip-python`, `rust-analyzer`, `scip-clang`,
  `scip-dotnet`, `scip-php`, `scip-ruby`, `scip-dart`. **[confirmed]**

**Bottom line on purity:** the *only* thing that must break pure-stdlib is the
**out-of-process indexer toolchain** (a build/CI concern), never the moedex
binary itself. Both tree-sitter-tags-via-CLI and SCIP-ingest avoid any cgo or
native dependency inside moedex. This is avoidable-by-design and the in-house
Waldo SCIP-sidecar pattern proves it works at corpus scale.

---

## moedex integration points (actual packages/types)

The content model is the right place to anchor this. Symbols attach to **blobs**
(`index.Blob`, keyed by git blob SHA), so a symbol extracted once is shared by
every `FileRef` the dedup'd content appears at — symbols dedup for free, exactly
like content does.

**1. New package `internal/symbol`.** Holds the symbol index and its codec,
parallel to how `internal/tokenindex` and `internal/embed` sit beside
`internal/index`. Persisted as a sidecar next to the `.moedex` file by
`internal/diskstore` (which already owns Save/Load and mmap), keyed by blob SHA so
it survives dedup and delta indexing.

Proposed data (deliberately minimal — byte ranges, to match `index` semantics):

```go
package symbol

// Symbol is one named definition within a blob. Ranges are BYTE offsets into
// Blob.Content (index is byte-addressed; convert SCIP's line/col at ingest).
type Symbol struct {
    Name       string // e.g. "Assemble"
    Kind       Kind   // Func, Method, Type, Const, Var, ...
    NameStart  int    // byte offset of the name token (for name-match ranking)
    NameEnd    int
    BodyStart  int    // byte offset of the enclosing definition's first byte
    BodyEnd    int    // byte offset just past the definition's last byte
}

// Index maps a blob to its symbols, sorted by BodyStart for binary search.
type Index struct { /* bySHA map[string][]Symbol, or by blob ID */ }

// Enclosing returns the innermost symbol whose [BodyStart,BodyEnd) covers off.
func (ix *Index) Enclosing(blob uint64, off int) (Symbol, bool)
```

**2. `contextwin` (the headline win).** `expandSpan` currently runs the
brace/indent heuristic the package docstring openly calls "honestly approximate —
there is no symbol layer yet." Replace step 1 with: convert the salient
`rank.LineSpan` to a byte offset via `Blob.lineStarts`, call
`symbol.Index.Enclosing`, and if it hits, set the block to `[BodyStart,BodyEnd)`
mapped back to lines via `Blob.LineOf`. **Fall back to the existing heuristic when
no symbol covers the span** (unsupported language, no sidecar, top-level code) —
so the change is purely additive and the dense arm / pure-stdlib mode still work
with zero symbol data. This directly delivers the architecture's "symbol-scoped
context blocks."

**3. `rank` (symbol-name as a retrieval arm).** The design calls for three arms
(exact/symbol/dense) fused. Add a third RRF input alongside `lexicalArm` and
`denseArm`: a `symbolArm` that matches query terms against `Symbol.Name` and
boosts blobs whose *symbol names* match (zoekt confirms "match is on a symbol" is
a top ranking signal — matches on a symbol score higher than ordinary text).
**[confirmed]** Because `Ranker.Rank` already fuses via RRF (needs no score
calibration), adding an arm is a contained change: emit `(blob, rank)` from the
symbol arm and add `1/(k+rank)` into the existing `agg.rrf`. The `LineSpan` it
contributes can be the symbol's name line, which then feeds `contextwin`.

**4. `mcp`.** No protocol change needed for slice 1 — better scoping flows
through automatically because `IndexSearcher.SearchContext` → `rank` →
`contextwin.Assemble` is unchanged in shape. A later slice could add a
`symbol_scope` field to `search_context` (return only the enclosing symbol) or a
new `goto_definition` tool once SCIP refs land.

**5. `ingest` / build pipeline.** Add an out-of-process step: after blobs are
collected, run `tree-sitter tags` (or `scip-go`) per file, map ranges to the
blob's byte space, and write the `internal/symbol` sidecar. Mirror the
multi-producer worker-fleet shape from the `waldo-cluster-reindex` playbook
(one worker per language) so it parallelizes and never blocks the trigram build.

---

## Proposed first slice (smallest valuable increment)

**Goal:** real enclosing-symbol scoping for Go files, syntactic only, no refs, no
ranking-arm yet. Prove the seam end-to-end on one language.

**Scope**
1. `internal/symbol`: `Symbol`/`Index` types above + a codec; persisted as a
   sidecar via `diskstore`, keyed by blob SHA.
2. Out-of-process extractor: a small `cmd/` tool or `ingest` step that shells out
   to `tree-sitter tags` for `.go` files (Go grammar ships `tags.scm`), parses
   its name/role/kind/range output, converts to byte ranges, writes the sidecar.
   **No parser linked into moedex.**
3. `contextwin.expandSpan`: consult `symbol.Index.Enclosing` first; fall back to
   the current brace/indent heuristic on a miss. Additive, fully backward
   compatible.
4. Tests: a fixture Go file with nested funcs/methods; assert blocks snap to real
   function boundaries where the old heuristic over/under-captured (e.g. braces
   inside strings/comments — the documented failure mode).

**Explicitly out of scope for slice 1:** find-refs, go-to-def, SCIP ingest, the
symbol ranking arm, languages beyond Go, any in-binary parser.

**Effort estimate [synthesis]:** ~2-4 focused days. `internal/symbol` + codec
(~0.5d), the extractor + range mapping (~1-1.5d, the fiddly part is line/col →
byte and matching tree-sitter's UTF semantics to moedex's byte offsets), the
`contextwin` wiring + fallback (~0.5d), tests + a real-corpus sanity check
(~0.5-1d). Low risk because it's additive and the heuristic remains the floor.

**Natural follow-ups (later slices):** (b) symbol-name ranking arm in `rank`;
(c) more languages via their `tags.scm` (near-free per language); (d) ingest
precomputed SCIP for actual go-to-def/find-refs and a `goto_definition` MCP tool.

---

## Risks & open questions

- **Range encoding mismatch [confirmed risk].** moedex is **byte**-addressed
  (`index.go` comment "byte-trigrams", `Posting.Offset` is a byte index, slice 4
  memory note). tree-sitter and SCIP both speak line/column with a
  `PositionEncoding` (UTF-8/16/32). The conversion to byte offsets is the main
  correctness hazard — multi-byte runes (the diskstore test already exercises
  `café`/`日本語`) will expose off-by-N bugs. Note: an *earlier* moedex comment
  (`index.go` package doc) says "rune-based" while the AddFile loop and slice-4
  memory say bytes — **confirm the live offset semantics before mapping ranges.**
- **Pure-Go tree-sitter maturity [synthesis].** `odvcencio/gotreesitter` is
  attractive (no cgo, fast) but its production maturity is unverified here. The
  subprocess-CLI path sidesteps this entirely for slice 1; only revisit
  in-process parsing if subprocess overhead at 8GB-corpus scale proves real.
- **SCIP build cost [confirmed].** `scip-go` needs a buildable `go.mod`; repos
  that don't build don't index. This is fine because SCIP is a *later* slice and
  out-of-process — a failed indexer just yields no sidecar, and `contextwin`
  falls back. Do not let it block the trigram build.
- **Staleness if SCIP is added [confirmed].** A precomputed SCIP artifact can lag
  content. Keying symbols by **blob SHA** (not path) bounds this: stale symbols
  simply won't match the current blob and the system falls back — content
  addressing turns staleness into a clean miss rather than wrong data.
- **Single-node assumption.** Sidecar-by-blob-SHA is distribution-friendly (same
  seam as the trigram store), so this doesn't hardcode single-node — but verify
  the sidecar can be sharded the same way the posting store will be.
- **Still-open (not resolved by this run) [from the initial design].** Right fusion for
  lexical+symbol+dense: RRF (current default, used by slice's symbol arm) vs a
  learned reranker. Slice 1 doesn't touch fusion, so this stays deferred.

---

## Sources

- [tree-sitter/go-tree-sitter (official Go bindings)](https://github.com/tree-sitter/go-tree-sitter) — cgo dependency, per-grammar `go get`, mandatory `Close()`; basis for the purity-cost analysis. Accessed 2026-06-22.
- [WebSearch: Go tree-sitter bindings 2026 landscape] — surfaced `odvcencio/gotreesitter` (pure-Go reimpl, parse-table compatible, faster incremental) and `malivvan/tree-sitter` (Wasm/wazero, pre-release) as the cgo-free options. 2026.
- [Tree-sitter Code Navigation / tags docs](https://tree-sitter.github.io/tree-sitter/4-code-navigation.html) — `tags.scm`, `@definition.kind`/`@reference.kind` roles, `tree-sitter tags` CLI output (name/role/location/docstring); confirms GitHub code-nav and aider consume tags. Accessed 2026-06-22.
- [SCIP repo + scip.proto](https://github.com/sourcegraph/scip/blob/main/scip.proto) — Index/Document/Occurrence/Symbol/SymbolInformation; 0-based ranges with PositionEncoding; SymbolRole bitset (Definition=0x1); symbol URI grammar; "pure protobuf data, no parser to consume." Accessed 2026-06-22.
- [Announcing SCIP (Sourcegraph)](https://sourcegraph.com/blog/announcing-scip) — SCIP replaces LSIF, ~4-5x smaller payloads, easier to produce; powers go-to-def/find-refs. Accessed 2026-06-22.
- [scip-go (now github.com/scip-code/scip-go)](https://github.com/scip-code/scip-go) — migrated to `scip-code` org, v0.2.x (May 2025), needs `go.mod`; confirms the indexer ecosystem (scip-typescript/java/python/clang/etc.). Accessed 2026-06-22.
- [zoekt + universal-ctags (DeepWiki / Sourcegraph zoekt)](https://deepwiki.com/google/zoekt) — zoekt extracts symbols via ctags at index time, `HasSymbols` flag, and "match is on a symbol" is a key ranking signal; original google/zoekt archived 2025-03-21. Supports the symbol-name ranking arm. Accessed 2026-06-22.
- moedex code read directly: `internal/index/index.go`, `internal/contextwin/contextwin.go`, `internal/rank/{rank,ranker}.go`, `internal/mcp/mcp.go`, `internal/diskstore/*`, `go.mod` (module moedex, go 1.26, stdlib-only). Accessed 2026-06-22.
- In-house: `waldo-cluster-reindex` skill (multi-producer out-of-process SCIP enrichment, sidecar-by-shard) — proves the ingest-precomputed-SCIP pattern at corpus scale.
