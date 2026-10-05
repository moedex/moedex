# ADR 0022: Official MCP SDK contract and snapshot-bound result identity

- **Status:** Accepted
- **Date:** 2026-08-24
- **Context owner:** moedex

## Context

Moedex's original MCP surface used a handwritten JSON-RPC lifecycle. It kept the
agent API small, but it also made protocol negotiation, Streamable HTTP request
metadata, discovery cache hints, typed output schemas, and cancellation behavior
local protocol code. Separately, hot reload could make a response's data and a
later metadata lookup describe different rank or graph generations.

Agents also need a content-level identity that survives process restarts and graph
rebuilds. Numeric blob IDs and graph generations are local allocation/reload
identities; neither is a source-content cache key.

## Decision

Use `github.com/modelcontextprotocol/go-sdk` v1.7.0 for MCP lifecycle and transport.
Moedex serves current protocol `2026-07-28` and caps initialize-era negotiation at
`2025-11-25`:

- current stateless Streamable HTTP requests use the required
  `MCP-Protocol-Version`, `Mcp-Method`, and (for tool calls) `Mcp-Name` metadata;
  disagreement between headers and the JSON-RPC envelope is rejected;
- header-less compatibility requests are pinned to `2025-11-25`, preventing an
  accidental fallback to pre-2025-06 batching semantics;
- both served revisions reject JSON-RPC batches;
- `server/discover` and `tools/list` advertise public five-minute cache hints;
- the tool catalog is fixed for a process and advertises `listChanged: false`;
- stdio and stateless Streamable HTTP share the same SDK server, tool catalog,
  limits, deadlines, cancellation, and panic isolation. HTTP keeps the existing
  bearer-auth and outer middleware composition.

`serverInfo.version` identifies the running binary, not an API constant:

- tagged: `<tag>+<12-character-commit>`;
- untagged: `dev+<12-character-commit>`;
- a modified canonical build appends `.dirty.<source-digest>`, where the full
  SHA-256 digest covers the Git-visible worktree paths, modes, deletions, and
  Git blob hashes;
- an ad-hoc dirty build without linker metadata appends `.dirty.unknown`, and
  `make install` / `make install-dense` reject dirty worktrees;
- unavailable VCS data is represented as `unknown`.

### Typed tool results

Every registered tool has an input schema, output schema, and annotations declaring
it read-only, non-destructive, idempotent, and closed-world. Tool schemas use a
single closed root object because host adapters may reject a whole catalog when a
root contains `oneOf` or `anyOf`. Each output schema advertises the superset of its
normal properties plus the optional standard error property; concrete payload
variants remain enforced by handlers and contract fixtures:

```json
{"error":{"code":"invalid_arguments","message":"..."}}
```

Every successful call returns both `structuredContent` and the existing text
`content` fallback. The compatibility `format` argument changes only the fallback
presentation; it never suppresses structured content. This applies to all 19 tools,
including `find_symbol` and `symbols_overview`, whose structured form is now
`{"status":"...","symbols":[...]}`.

Initialization and discovery instructions tell clients to use `search_context` with
a token budget, treat `Pattern` as the default graph confidence floor, compare graph
identity tuples before composing calls, prefer `blob_sha` for content caches, and
distinguish navigation `ready_empty`, `unsupported`, and `unavailable`.

### Snapshot metadata

Handlers do not build `_meta` maps. A shared result builder stamps every tool result
under the stable vendor key `dev.moedex/snapshot`:

```json
{
  "cacheable": true,
  "corpus_fingerprint": "...",
  "graph_corpus_fingerprint": "...",
  "graph_generation": 5,
  "graph_build_id": "...",
  "blob_shas": ["..."],
  "unanchored_paths": ["..."]
}
```

The same builder also stamps `_meta["dev.moedex/server"]` with the running
`serverInfo.version`. This identifies pre-snapshot argument errors and legacy tool
responses even when standard response-server metadata is unavailable.

Irrelevant fields are omitted. `graph_corpus_fingerprint` appears only when a fused
search response's acquired graph corpus differs from its acquired rank corpus; this
keeps a reload tear observable instead of overwriting either identity.

Metadata is collected while the same reference-counted rank or graph snapshot that
produced the response remains acquired. Search returns its context window and rank
identity across one holder boundary. Graph traversal, annotations, discovery, and
clusters similarly return data and identity from one graph acquisition. Metadata
collection must never ask a holder for the "current" snapshot a second time.

`blob_shas` is the sorted, unique set of source blobs consumed by returned data:
search blocks, graph nodes and edge evidence, discovery source/symbol results,
cluster members, and navigation files as applicable. Graph identity is the tuple
`(corpus_fingerprint, graph_generation, graph_build_id)`; clients must compare the
tuple before combining multiple graph calls. `blob_sha` remains the preferred
content-level cache key.

### Content hashing and cacheability

Indexed results use the Git blob SHA already stored in the corpus. Live navigation
hashes the queried file and every unique returned file from its bytes with Git's
canonical object algorithm:

```text
SHA-1("blob " + decimal_byte_length + NUL + file_bytes)
```

Navigation reads are bounded, parallel, and cancellation-aware. An unreadable file
does not discard otherwise useful locations or symbols: its path is added to
`unanchored_paths` and the result is marked `cacheable: false`. Every tool result
with `isError: true` is non-cacheable even if it retains an acquired snapshot for
diagnosis. Successful graph/rank results require their producing snapshot;
successful navigation requires at least one blob anchor and no unanchored path.
Consequently anchored `ready_empty` and `unsupported` navigation answers may be
cached, while `unavailable` may not. Consumers may cache only when `cacheable` is
true and should key content by the returned SHA values.

## Consequences

The wire lifecycle follows the official SDK while Moedex retains its existing
handlers, authentication, limits, and text rendering. Typed results can be validated
directly against discovery, and cache consumers can detect cross-reload tears rather
than silently composing mismatched data. The additional metadata and schemas enlarge
responses slightly, and navigation hashing performs bounded file reads, but neither
change requires a corpus reindex or graph rebuild.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related

[0004](./0004-content-addressable-blob-store.md),
[0009](./0009-agent-context-api.md),
[0010](./0010-warm-serving-spine.md),
[0015](./0015-structured-context-result.md), and
[0017](./0017-lsp-navigation-and-the-serena-boundary.md).
