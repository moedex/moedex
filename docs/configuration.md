# Moe configuration

Moe has one typed registry for production runtime settings. Existing
`MOEDEX_*` environment names are compatibility contracts; the unified shell does
not rename them.

## Sources and precedence

Highest precedence wins:

1. a command flag;
2. a file named explicitly with `--config FILE`;
3. the ambient environment;
4. the built-in default.

There is no implicit file discovery. A configuration file contains one
`KEY=VALUE` entry per line. Blank lines and lines beginning with `#` are ignored,
`export ` is optional, and one pair of matching single or double quotes is
removed. Values are not shell-expanded.

```sh
# /etc/moedex/moe.env
MOEDEX_SHARD_DIR=/srv/moedex/shards
MOEDEX_HTTP_ADDR=127.0.0.1:8080
MOEDEX_AUTH_TOKEN='replace-with-a-secret'
```

```sh
moedex --config /etc/moedex/moe.env config --diff
moedex serve --config /etc/moedex/moe.env
moedex config --json --diff
```

An unknown `MOEDEX_*` key in an explicit file is fatal and includes a closest
match when available. An unknown ambient key produces a warning. Invalid typed
values are fatal. `ONNXRUNTIME_LIB_PATH` and `GITLAB_TOKEN` are accepted
pass-through settings because their owning external tools define them.

`moedex config` shows effective values and provenance. `--diff` limits output to
non-default sources. `--json` emits a versioned schema; secret values such as the
auth token and TLS key are always redacted.

## Registry

| Environment key | Type/default | Commands | Meaning |
|---|---|---|---|
| `MOEDEX_CORPUS` | string | corpus, index, parity | Managed corpus root |
| `MOEDEX_CAS_DIR` | string | index cas | Content-addressable store root |
| `MOEDEX_SHARD_DIR` | string | search, serve, mcp, graph, doctor | Servable shard directory |
| `MOEDEX_INDEX_DIR` | string | search, serve, mcp, index snapshot, doctor | Atomic snapshot root |
| `MOEDEX_AUTH_TOKEN` | secret string | serve | HTTP and MCP bearer token |
| `MOEDEX_HTTP_ADDR` | string | serve | Retrieval HTTP bind address |
| `MOEDEX_MCP_HTTP_ADDR` | string | serve, doctor | MCP HTTP bind address |
| `MOEDEX_SEARCH_MAX_CONCURRENCY` | integer, `8` | serve | Maximum concurrent HTTP searches |
| `MOEDEX_MCP_MAX_CONCURRENCY` | integer, `8` | serve | Maximum concurrent MCP requests |
| `MOEDEX_EMBED` | enum, `auto` | serve, mcp, index | `auto`, `onnx`, `http`, or `none` |
| `MOEDEX_EMBED_URL` | string | serve, mcp | HTTP embedding service URL |
| `MOEDEX_EMBED_MODEL` | string | serve, mcp | HTTP embedding model identifier |
| `MOEDEX_ONNX_INTRA_OP_THREADS` | integer, `0` | serve, index | Threads within ONNX operators |
| `MOEDEX_ONNX_INTER_OP_THREADS` | integer, `0` | serve, index | Threads across ONNX operators |
| `MOEDEX_TLS_CERT` | string | serve | TLS certificate path |
| `MOEDEX_TLS_KEY` | secret string | serve | TLS private-key path |
| `MOEDEX_GRAPH_SIMILAR_TOP_K` | integer, `5` | graph, index | Semantic graph neighbors per symbol |
| `MOEDEX_GRAPH_SIMILAR_THRESHOLD` | float, `0.60` | graph, index | Semantic graph similarity floor |
| `MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT` | integer, `4096` | graph, index | Exact semantic comparison limit |
| `MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES` | integer, `2048` | graph, index | Maximum semantic graph candidates |
| `MOEDEX_GRAPH_LSP_CONCURRENCY` | integer, `32` | graph, index | Concurrent graph LSP requests |
| `MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND` | float, `100` | graph, index | Graph LSP request start rate |
| `MOEDEX_GRAPH_CLUSTER_MAX_NODES` | integer, `250000` | graph, doctor | Maximum nodes considered for clustering |
| `MOEDEX_VERIFY_CONTENT` | boolean, `true` | serve, index | Verify content hashes while loading |
| `MOEDEX_LSP_DEBUG` | boolean, `false` | nav, serve, graph | Enable LSP protocol debugging |
| `MOEDEX_LSP_WORKSPACE_DIR` | string | nav, serve, graph, index | Writable isolated workspace cache for managed-corpus language servers; defaults beneath `MOEDEX_INDEX_DIR` or `~/.moedex-state` |
| `MOEDEX_MMAP` | boolean, `false` | corpus scale | Measure mmap reload during scale runs |
| `MOEDEX_SELECTIVE` | float | corpus scale | Selective trigram maximum document fraction |

Test, benchmark, evaluation, and deployment-only variables are intentionally not
part of the production registry. Their owning harnesses continue to validate them.
