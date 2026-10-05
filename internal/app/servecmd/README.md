# `moe serve`

The warm retrieval daemon. It memory-maps a directory of prebuilt shards **once**
and answers queries with zero cold-start, in one of five modes:

| Mode | Flag | Surface | What it serves |
|------|------|---------|----------------|
| Retrieval daemon | `-http :8080` | HTTP JSON (`/search`, `/stats`, `/healthz`, `/metrics`) | line-granular literal/regex matches, parity-proven against ripgrep |
| One-shot query | `-q PATTERN` | stdout (`repo/relpath:line`) | a single retrieval query, for validation/scripting |
| Ranked agent context (stdio) | `-mcp` | MCP over stdio (`search_context` + graph tools) | ranked, deduplicated, token-budgeted context blocks, each fused with its graph neighborhood; spawns per agent session |
| Ranked agent context (HTTP) | `-mcp-http :8081` | MCP over Streamable HTTP at `/mcp` | the same `search_context` tool, loaded once and shared across sessions over the network |
| Dense sidecar refresh | `-build-embeddings` | stdout/stderr, then exit | builds/refreshes the corpus embedding sidecar for `-shard-dir` out of band, so a warm daemon reload never re-embeds inline |

Provide either `-shard-dir` (`MOEDEX_SHARD_DIR`) or the atomic snapshot root
`-index-dir` (`MOEDEX_INDEX_DIR`). The latter resolves `CURRENT` at boot and on
SIGHUP; the two flags are mutually exclusive. `-build-embeddings` intentionally
requires `-shard-dir`, because a published snapshot is immutable. With
none of `-mcp`/`-mcp-http`/`-http`/`-q`/`-build-embeddings`, the process exits with
usage on stderr. `moe version` prints build identity and compiled dense capability.

## Quick start

```sh
# Retrieval daemon
moe serve -shard-dir /path/to/shards -http 127.0.0.1:8080

# One-shot literal query (add -regex for a pattern)
moe serve -shard-dir /path/to/shards -q "func main" -regex

# Ranked agent context over MCP (lexical + symbol arm, no extra deps)
moe serve -shard-dir /path/to/shards -mcp

# Ranked agent context over MCP/HTTP — warm shared daemon for coding agents
moe serve -shard-dir /path/to/shards -mcp-http :8081

# The same daemon over an immutable snapshot selected by index/CURRENT
moe serve -index-dir /path/to/index -mcp-http :8081

# Build/refresh the dense embedding sidecar out of band, then exit
moe serve -shard-dir /path/to/shards -build-embeddings -embed onnx
```

On the two warm modes the daemon prints a boot line to stderr, e.g.
`mmap'd 12 shards, 480123 blobs in 8ms`.

## What a shard dir is

A shard dir holds one or more `*.idx` files plus a `manifest.json` freshness
sidecar. Produce one with the offline indexer:

```sh
moe index build -corpus /path/to/corpus-root -shard-dir /path/to/shards
```

It discovers every git repo under the corpus root, ingests them into a sequence
of content-sized shards (`shard-0000.idx`, `shard-0001.idx`, … — flushed at
`DefaultShardBytes` ≈ 150 MB of indexed content each), and writes the manifest
alongside. The result is directly servable as `-shard-dir`; `moe index check`
and `moe index refresh` maintain it.

`serve.Open(dir)` globs `*.idx` in sorted filename order and `mmap`s each shard,
holding the mappings for the process lifetime — so postings never enter the Go
heap and queries pay only decode-on-touch. Each shard is mapped via
`diskstore.LoadMmapDeduped` (sharing a corpus-wide content-store mapping) for a
deduped (MOEDEX05) shard dir, or `diskstore.LoadMmap` for a legacy
inlined-content shard. After a `Close` the corpus must not be queried (the
memory is unmapped); `Close` is idempotent.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | _(off)_ | unified-shell flag: load an explicit typed `KEY=VALUE` file before command flags; **flag > file > env > default** |
| `-shard-dir` | `$MOEDEX_SHARD_DIR` | directory of prebuilt `*.idx` shards (required unless `-index-dir` is used) |
| `-index-dir` | `$MOEDEX_INDEX_DIR` | immutable snapshot root; resolve `CURRENT` at boot/SIGHUP (mutually exclusive with `-shard-dir`) |
| `-http` | `$MOEDEX_HTTP_ADDR` | serve the retrieval HTTP API on this address (e.g. `127.0.0.1:8080`) |
| `-mcp` | `false` | serve ranked agent context over MCP (stdio) |
| `-mcp-http` | `$MOEDEX_MCP_HTTP_ADDR` | serve the ranked agent-context MCP tool over Streamable HTTP at `/mcp` on this address (e.g. `127.0.0.1:8081`) — the warm shared daemon for coding agents |
| `-q` | _(off)_ | one-shot retrieval query |
| `-build-embeddings` | `false` | build/refresh the corpus embedding sidecar for `-shard-dir`, then exit (requires `-embed onnx\|http`) |
| `-allow-insecure` | `false` | explicitly permit a tokenless non-loopback HTTP or MCP bind |
| `-regex` | `false` | treat `-q` / the `/search` query as a regular expression (default: literal) |
| `-limit` | `0` | cap matches printed/returned (`0` = no cap) |
| `-top-k` | `20` | default ranked results per MCP query |
| `-embed` | `$MOEDEX_EMBED` (`auto`) | dense embedder for `-mcp`: `auto`\|`onnx`\|`http`\|`none` |
| `-onnx-runtime` | `$ONNXRUNTIME_LIB_PATH`, then standard Homebrew/system paths | path to the ONNX Runtime shared library (in-process embedder; requires an `-tags onnx` build) |
| `-onnx-intra-op-threads` | `$MOEDEX_ONNX_INTRA_OP_THREADS` (`0`) | CPU threads within ONNX operators; `0` keeps the runtime default |
| `-onnx-inter-op-threads` | `$MOEDEX_ONNX_INTER_OP_THREADS` (`0`) | CPU threads across independent ONNX graph operators; `0` keeps the runtime default |
| `-auth-token` | `$MOEDEX_AUTH_TOKEN` | if set, require `Authorization: Bearer <token>` on `-http` (except `/healthz`, `/metrics`) |
| `-tls-cert` | `$MOEDEX_TLS_CERT` | TLS certificate file; serve `-http` over HTTPS (requires `-tls-key`) |
| `-tls-key` | `$MOEDEX_TLS_KEY` | TLS private key file; serve `-http` over HTTPS (requires `-tls-cert`) |
| `-request-timeout` | `30s` | per-request HTTP timeout on `-http` (`503` on expiry) |
| `-search-max-concurrency` | `$MOEDEX_SEARCH_MAX_CONCURRENCY` (`8`) | cap concurrent in-flight `/search` requests on `-http` (`503` when full); `0` disables the cap |

### Configuration file (`--config`)

`--config FILE` loads settings from a typed `KEY=VALUE` file before command flags
are applied. One `VAR=value` appears per line; `#` comments and blank lines are
ignored, and surrounding quotes are optional:

```
# /etc/moedex/moedex-serve.env
MOEDEX_SHARD_DIR=/srv/moedex/shards
MOEDEX_HTTP_ADDR=127.0.0.1:8080
MOEDEX_AUTH_TOKEN=s3cret
# MOEDEX_TLS_CERT=/etc/moedex/tls/cert.pem
# MOEDEX_TLS_KEY=/etc/moedex/tls/key.pem
# MOEDEX_EMBED=onnx
```

Then run `moe serve --config /etc/moedex/moedex-serve.env`. Precedence is **flag
> file > env > built-in default**. Unknown `MOEDEX_*` keys in the explicit file
are fatal. See [../../../docs/configuration.md](../../../docs/configuration.md)
for the complete registry. Under systemd, `EnvironmentFile=` can read the same
file directly.

## Retrieval daemon (`-http`)

A minimal JSON API backed by `serve.Corpus`. It runs until `SIGINT`/`SIGTERM`,
then shuts down gracefully (5 s drain); `SIGHUP` hot-reloads the shards (see
[Hot reload](#hot-reload-sighup)). It binds **loopback by default** (see
[Bind address](#bind-address-loopback-default--behavior-change)), can require a
[bearer token](#authentication), serve [TLS](#tls) directly, bound each request
with a [timeout](#per-request-timeout), and expose Prometheus
[`/metrics`](#get-metrics).

### `GET /search`

Query params:

- `q` — the pattern (**required**; missing → `400` with `{"error": "..."}`)
- `regex` — `1` or `true` to treat `q` as a regex (default: literal)
- `limit` — positive integer to cap returned matches (`count` still reports the
  full total)

Response:

```json
{
  "query": "func main",
  "regex": true,
  "count": 42,
  "matches": [
    {"repo": "myrepo", "rel_path": "cmd/x/main.go", "abs_path": "/abs/cmd/x/main.go", "line": 7}
  ],
  "stats": {"candidate_blobs": 128, "candidate_bytes": 81920, "query_all": false}
}
```

A malformed regex returns `400` with the parse error in `error`. Matches are
returned in a deterministic repo / rel-path / line order regardless of shard
layout, because results from independent shards merge by concatenation —
`search.Match` carries absolute/repo/relative paths, so there is no cross-shard
blob-ID space to reconcile.

### `GET /stats`

```json
{"shards": 12, "blobs": 480123}
```

### `GET /healthz`

`200` with body `ok`.

### How fan-out works

`Corpus.Regex` / `Corpus.Literal` run the per-shard query across all shards
concurrently (bounded by `runtime.NumCPU`), then merge and sort. The aggregated
`search.Stats` sums per-shard candidate blobs/bytes/lines; `query_all` is true
only when **every** shard fell back to scanning all candidates.

### Bind address (fail closed)

Use an explicit loopback address for a tokenless local daemon, such as
`127.0.0.1:8080` or `[::1]:8080`. Empty-host/wildcard syntax such as `:8080` is
rejected in tokenless mode, as are explicit non-loopback addresses. The requested
and effective bind addresses are logged at boot.

To expose the daemon beyond loopback, give an explicit host and configure a
bearer token:

```sh
MOEDEX_AUTH_TOKEN=s3cret moe serve -shard-dir DIR -http 0.0.0.0:8080
MOEDEX_AUTH_TOKEN=s3cret moe serve -shard-dir DIR -http 192.168.1.5:8080
```

The safety check applies to both `-http` and `-mcp-http`. A tokenless
non-loopback listener is rejected before opening a socket.
`-allow-insecure` is the explicit acknowledgement for isolated environments where
another boundary provides equivalent protection.

### Authentication

`/search` and `/stats` can require a bearer token; `/healthz` and `/metrics`
are **always open** (so liveness probes and Prometheus scrapes work without a
credential).

Configure the token by flag or env (the flag wins):

```sh
export MOEDEX_AUTH_TOKEN=s3cret      # base
moe serve ... -auth-token s3cret  # -auth-token overrides MOEDEX_AUTH_TOKEN
```

Precedence: `MOEDEX_AUTH_TOKEN` is the base value; a non-empty `-auth-token`
overrides it. With no token configured, `/search` and `/stats` are open only on a
loopback listener unless `-allow-insecure` explicitly acknowledges external
exposure.

Authenticated requests send `Authorization: Bearer <token>`. A missing or wrong
token yields `401` with `{"error":"unauthorized"}` and a `WWW-Authenticate: Bearer`
header.

### TLS

Serve HTTPS directly by giving both a cert and a key (they are all-or-nothing —
setting exactly one is a usage error):

```sh
MOEDEX_AUTH_TOKEN=s3cret moe serve -shard-dir DIR -http 0.0.0.0:8443 \
  -tls-cert /path/cert.pem -tls-key /path/key.pem
```

Alternatively, terminate TLS at a reverse proxy (nginx/Caddy/envoy) and keep
the daemon on loopback HTTP — often simpler operationally and the recommended
path when a proxy is already in front of it.

### Per-request timeout

`-request-timeout` (default `30s`) bounds each HTTP response: on expiry the
client gets `503`. Server-side `Read`/`Write`/`Idle` timeouts also guard against
slow clients.

On expiry, `withTimeout` cancels `r.Context()`, which `Corpus.Regex`/`Corpus.Literal`
thread into the search path: the scan's hot loops check cancellation on a stride,
so an expired request stops burning CPU promptly rather than running to
completion. The abort is observed within one cancellation-check stride, not
instantly — bounded, not immediate.

### Search concurrency cap

`-search-max-concurrency` (default `8`, override via `$MOEDEX_SEARCH_MAX_CONCURRENCY`
or the flag) bounds how many `/search` requests run at once. Each one scans the
full corpus and can pin a core for up to `-request-timeout`; without a cap, N
concurrent broad queries saturate every core and starve every other request,
including `/healthz` and `/metrics`. Once the cap is reached, the next request
is rejected immediately with `503` (no queuing) rather than waiting its turn —
mirroring the MCP server's own request-concurrency guard. `0` disables the cap.
Rejections are counted in `moedex_http_search_rejected_total` (see `/metrics`
below). `/stats`, `/healthz`, and `/metrics` are cheap and not subject to this
cap.

### `GET /metrics`

Prometheus text exposition (`Content-Type: text/plain; version=0.0.4; charset=utf-8`),
**unauthenticated** like `/healthz`. Stdlib-only — no `client_golang`. Exposes:

- `moedex_http_requests_total{code="2xx|4xx|5xx"}` — counter
- `moedex_http_panics_total` — counter (handler panics recovered)
- `moedex_reloads_total{result="ok|fail"}` — counter (SIGHUP reloads)
- `moedex_http_search_rejected_total` — counter (`/search` requests rejected by
  [the concurrency cap](#search-concurrency-cap))
- `moedex_http_request_duration_seconds` — histogram (fixed buckets + `_sum`/`_count`)
- `moedex_corpus_shards`, `moedex_corpus_blobs` — gauges, read from the live
  corpus at scrape time (so they track hot swaps)

### Observability

All daemon logging on `-http` is structured JSON on stderr (`log/slog`): boot,
listening, per-request `access` lines (method/path/status/bytes/dur_ms/remote),
reloads, and shutdown.

## One-shot query (`-q`)

Runs a single query against the warm corpus and prints `repo/relpath:line`, one
per line, with a `N match(es)` summary on stderr. Add `-regex` for a pattern;
`-limit N` truncates the printed list. Useful for scripted spot-checks and parity
validation.

## Ranked agent context (`-mcp`)

`-mcp` serves the `search_context` MCP tool over stdio, backed by
`serve.RankCorpus` — the ranked, corpus-wide surface.

`RankCorpus` loads only blob **content** from every shard (`diskstore.LoadBlobs`
— no positional postings, so no RAM wall), concatenates it into one index with
global blob IDs, and builds the ranking layer: a BM25 token index plus a
syntactic symbol index, fused by `rank.Ranker`. Candidate generation runs off the
token index (`Ranker.UseTokenCandidates`), which is why the content-only index
needs no trigram postings. It implements `mcp.ContextSearcher`, so the tool serves
the whole corpus, not a single repo.

The boot line reports the shape, e.g.:
`corpus ranker ready — 480123 blobs, 480123 docs, 120456 symbol blobs, 0 dense chunks (none) in 1.2s`.

### The `search_context` tool

```json
{
  "name": "search_context",
  "arguments": {"query": "csr validation", "token_budget": 4000, "top_k": 10,
                "format": "structured", "graph_depth": 1,
                "min_confidence": "Pattern"}
}
```

Only `query` is required; `token_budget`, `top_k`, `format`, `graph_depth`, and
`min_confidence` are
optional (`top_k` defaults to `-top-k`). An empty query is reported as a tool-level
error rather than a protocol error.

All 19 tools advertise both input and output JSON schemas. Every call returns typed
`structuredContent` plus the existing text fallback; `format` changes only that text
presentation. Tools are annotated read-only, non-destructive, idempotent, and
closed-world. Schema roots are closed objects without composition keywords for host
portability; each output lists its success properties plus the optional structured
error `{"error":{"code":"...","message":"..."}}`. Runtime validation remains
authoritative for constraints such as `impact_analysis` requiring exactly one of
`file` or `symbol`.

Every result also carries `_meta["dev.moedex/snapshot"]`. Rank/graph responses name
the exact acquired corpus fingerprint and graph generation/build ID; returned source
is anchored by sorted, unique Git `blob_shas`. Compare the full graph tuple before
combining separate calls and prefer `blob_sha` for content cache keys. Live navigation
hashes the queried and returned files using Git's blob SHA-1. Unreadable files remain
in the useful result under `unanchored_paths` and force `cacheable:false`. Tool
errors are always non-cacheable; anchored `ready_empty` and `unsupported` navigation
answers remain cacheable. `_meta["dev.moedex/server"]` identifies the running binary
on every success and error, independently of whether a data snapshot was acquired.

The MCP lifecycle uses the official Go SDK. Streamable HTTP discovery serves current
protocol `2026-07-28`; initialize-era clients negotiate at `2025-11-25`. Both reject
JSON-RPC batches. Discovery and `tools/list` are publicly cacheable for five minutes,
and the process-fixed catalog advertises `listChanged:false`. Current HTTP requests
must supply `MCP-Protocol-Version`, `Mcp-Method`, and `Mcp-Name` for tool calls.
`serverInfo.version` is the running build (`tag+commit` or `dev+commit`, with
`.dirty.<source-digest>` appended for canonical modified builds). Direct unstamped
dirty builds report `.dirty.unknown`; canonical install targets require a clean
worktree.

The token estimate is a hard upper bound: `token_estimate` never exceeds the
requested `token_budget`. If the best symbol/source block is too large it is narrowed
around the highest-ranked salient line and reports `clipped: true` on both the block
and summary. `truncated: true` has the separate meaning that lower-ranked candidate
blocks were omitted; both flags can be true in one response.

### Graph-fused results

Every returned block is annotated with its **graph neighborhood**, so an agent that
found a symbol does not need a second tool call to learn what calls it or what it
depends on:

| Bucket | Contains |
|---|---|
| `callers` | symbols that call the block's symbols (incoming `calls`) |
| `callees` | symbols the block's symbols call (outgoing `calls`) |
| `consumers` | handlers that consume the block's event/message (incoming `consumes`) |
| `publishers` | publishers of the block's event/message (incoming `publishes`) |
| `depends_on` | outgoing `imports` / `uses_type` / `references` / `candidate` / `publishes` / `consumes` |
| `similar_to` | semantic siblings (`similar_to`, either direction; needs an `onnx`-built graph) |

Each neighbor carries its node `id`, symbol, kind, repo/path/line, the `edge` type and `direction`
that reached it, the hop count, and the edge's confidence `{tier, score}`.
Text renders this as `Symbol [Tier] (repo/path:line)`.

Follow a neighbor with `trace_calls` using its `symbol`, `repo`, and exact
repository-relative `path`. Repository and path select starting declarations;
the traversal can still cross repositories. Omit both selectors to trace every
declaration with that name. An unmatched selector returns an empty result.
The node `id` identifies returned evidence; it is not a `symbol` argument.

`graph_depth` controls the radius: **1** by default (the direct neighborhood), up to
10, and **0 turns the annotation off**. Each lane traverses in one fixed direction, so
`graph_depth: 2` means "callers of callers", not an undirected blob. Both text
fallback presentations carry it as one `[graph] ...` line under each block header,
and structured output always carries a typed `neighbors` object per block.

`min_confidence` accepts `Candidate`, `Pattern`, `Verified`, or `Proven` and defaults
to `Pattern`. The same argument is available on every standalone traversal tool.
Edges below the floor are removed before traversal, so they cannot appear or bridge
to a later node. Candidate evidence remains available for explicit diagnostics.

Two deliberate limits, so a short answer is never mistaken for a small neighborhood:
buckets retain at most 25 neighbors and expose `bucket_totals` with the true
pre-cap count for all six lanes. The compatibility `truncated` flag is derived from
those totals; text omissions render as `+N more (of M total)`. Incoming
reference/import edges are not annotated at all — that is the highest-volume edge
class in the graph, and `impact_analysis` is the tool for that question. A block whose
symbols have no edges gets a present, empty annotation, so "annotation off" and
"nothing found" stay distinguishable.

The annotation needs the `corpus-graph.graph` sidecar that `moe graph build` writes
beside the shards; without it (for example, single-repo `moe mcp --repo`), `graph_depth` is
accepted and inert.

### Standalone graph tools and clusters

Standalone `GraphNode` results include `hops` and are ordered by hop, confidence,
same-repository/shared-directory proximity to the root, symbol, then stable node ID.
All standalone graph traversals default to the same `Pattern` confidence floor.

`graph_schema` identifies the exact loaded snapshot as well as describing it. Its
structured payload includes `generation`, the content-true `corpus_fingerprint`
used by ranking sidecars, and `build_id` (SHA-256 of the complete graph artifact).
Use those three fields as the graph portion of a downstream cache key; callers do
not need to couple cache discovery to `list_clusters`.

`list_clusters` never computes Louvain while serving. Graph build/refresh writes the
generation-bound `corpus-graph.clusters.json` sidecar from only Verified/Proven
`calls`, `http_calls`, `imports`, and manifest `depends_on` edges. A summary call is
`{"offset":0,"limit":50}`; a member call is
`{"cluster_id":1,"offset":0,"limit":50}`. Limits default to 50 and max at 200.
The default build cap is 250,000 eligible nodes, configurable with
`MOEDEX_GRAPH_CLUSTER_MAX_NODES`; absent, stale, or over-cap sidecars return an
explicit unavailable envelope with counts and rebuild/configuration guidance.

Rebuild and verify these artifacts with:

```sh
go run ./cmd/moe graph build -shard-dir /path/to/shards
go run ./cmd/moe graph audit -shard-dir /path/to/shards -sample 50
go run -tags lsp ./cmd/moe graph audit -shard-dir /path/to/shards -resolve-sample 20
make graph-eval
MOEDEX_GRAPH_EVAL_SHARDS=/path/to/shards MOEDEX_GRAPH_GOLD=/path/to/reviewed-gold.json make graph-eval-private
```

The hermetic graph gate is also part of `make health`; the private command requires
at least 30 reviewed records and committed mechanical floors in its mounted gold file.
`graph-audit` scans the current mmap graph without rebuilding or writing it and reports
Pattern call-site fanout plus References-vs-Definition request projections. The optional
LSP sample is wall-clock bounded, reports typed response counts and latency, and never
persists its Proven results. Production LSP graph builds use only participating Pattern
targets from the symbol sidecar; they do not run the former all-file `DocumentSymbol`
sweep. A group without an exact positive—or with partial, unsupported, unavailable,
or multi-context coverage—retains the existing Pattern siblings; an empty response
alone never suppresses them.

The MCP server applies hardening defaults around the SDK: a 30 s per-call timeout,
8-way handler concurrency, a 1 MiB cap on a single request, and an 8 KiB cap on the
query string. Request cancellation propagates through Streamable HTTP and into
search, graph, and navigation work; panic isolation prevents a handler failure from
terminating the process.

## The dense arm

At query time, an unavailable dense backend preserves lexical, symbol and path
context. MCP returns `summary.warnings: ["dense_unavailable"]` and a fixed text
notice, and marks that answer non-cacheable. Backend details are not included.
Cancellation and deadline errors still fail the request. A disabled or
query-gated dense arm is normal operation and emits no outage warning.

Exact dense scoring uses worker-local top-K heaps instead of an array sized to
the whole chunk corpus. A process-wide scoring budget, set to `GOMAXPROCS` on
first dense search, bounds workers across concurrent requests and overlapping
serving generations. Waiting and scoring honor cancellation. This budget does
not include embedding generation or offline similarity construction.

The dense (embedding) arm is **`-mcp` only** and **optional**. When it is off,
ranking is pure lexical (BM25) + symbol arm with zero external dependencies. The
`-embed` flag selects the embedder:

| `-embed` | Behavior |
|----------|----------|
| `auto` (default) | `onnx` if a configured or standard Homebrew/system runtime is found, else `http` if `MOEDEX_EMBED_URL` is set, else `none` |
| `onnx` | in-process embedder (requires an `-tags onnx` build — see below) |
| `http` | external OpenAI/ollama-style embeddings endpoint |
| `none` | dense arm disabled (lexical + symbol only) |

Any dense setup or build failure degrades **cleanly** to lexical + symbol with a
message on stderr — a down embedding service, a missing ONNX runtime, or a binary
built without the `onnx` tag will never take the ranker down.

### In-process embedder (`-embed onnx`)

The `onnx` embedder runs a code-trained sentence encoder in-process — no external
service. The model (`st-codesearch-distilroberta-base`, CodeSearchNet-trained,
768-d, int8-quantized to ~78 MB) and its tokenizer are embedded directly in the
binary. It is compiled in only under the `onnx` build tag, so the **default build
stays pure-Go with zero ML dependencies**.

Build it with:

```sh
make build-dense          # go build -tags onnx -o moe-dense ./cmd/moe
```

At run time it needs the ONNX Runtime **shared library** (the model weights are
bundled, the runtime is not). Install it and point the daemon at it:

```sh
brew install onnxruntime  # provides libonnxruntime.dylib

./moe-dense mcp -shard-dir /path/to/shards -embed onnx \
  -onnx-runtime /opt/homebrew/Cellar/onnxruntime/<version>/lib/libonnxruntime.dylib
# or: export ONNXRUNTIME_LIB_PATH=.../libonnxruntime.dylib
```

`make test-dense` runs the `onnx`-tagged embedder test, which skips cleanly when
the runtime library is absent.

### HTTP embedder (`-embed http`)

Talks to a local OpenAI/ollama-style embeddings endpoint. Configure it with:

- `MOEDEX_EMBED_URL` — base URL (**required** for `http`; the daemon errors if unset)
- `MOEDEX_EMBED_MODEL` — model name (recorded in the embedding cache meta)

It issues `POST {MOEDEX_EMBED_URL}/embeddings` with body
`{"model": <model>, "input": [<texts...>]}` and parses the OpenAI-style
`{"data": [{"embedding": [...]}, ...]}` response, preserving order. The embedding
dimension is discovered from the first response.

### Embedding persistence

Embedding the whole corpus at boot is the dense arm's one-time cost. To avoid
paying it on every boot, the chunk embeddings are persisted to a sidecar next to
the shards:

- `corpus-embeddings.store` — the embedding store
- `corpus-embeddings.store.meta` — a validating header: the shard-set fingerprint
  (sha256 over each shard's sorted basename + byte size), the embedding model, and
  the unified-index blob count

On boot, if the `.meta` still matches the current corpus, the store is loaded
**instantly**; otherwise the corpus is re-embedded and the store is re-saved. Any
change to the shard set, the model, or the blob count invalidates the cache.
Persistence is best-effort — a failed cache write logs and continues with a
working ranker. The boot line reports the dense source as `cached`, `built`, or
`none`.

## Freshness

The shard set carries a `manifest.json` (schema in `internal/parity`) recording,
per shard, which repos contributed blobs to it, and per repo, its git HEAD at
ingest. `moe index` drives shard-level partial re-indexing off it:

```sh
moe index check   -shard-dir /path/to/shards   # dry run: changed/added/removed repos
moe index refresh -shard-dir /path/to/shards   # rebuild affected shards, swap in place
```

Under the hood:

1. `check` (`DetectChanges`) compares each repo's current `git rev-parse HEAD`
   against the manifest to classify repos as changed / added / removed (an
   unreadable HEAD is treated as changed — rebuild rather than serve stale). The
   corpus root defaults to the one recorded in the manifest; `-corpus` overrides.
2. `refresh` (`Rebuild`) rebuilds only the shards whose repo set intersects the
   affected repos (re-ingesting every repo that shared those shards, to preserve
   in-shard dedup), copies untouched shards forward, writes a fresh manifest into
   a new dir, and atomically swaps it into place (the old dir is removed unless
   `-keep-backup`).

Because shards are flushed at a content-byte threshold, the shard numbering of a
rebuilt region is reassigned per build — treat shard IDs as opaque, not stable
across rebuilds.

### Hot reload (SIGHUP)

Rank and graph readers acquire independently. When graph-enriched context sees
different source corpus fingerprints, MCP returns the non-cacheable tool error
`snapshot_unavailable` without the mixed evidence. Retry after publication or
use `graph_depth=0` for source-only retrieval. A graph-only rebuild over the same
source corpus remains compatible. This check applies to fingerprint-bearing
adapters; legacy/custom adapters without fingerprints cannot provide that
guarantee. Atomic combined acquisition and cross-call snapshot pinning remain
planned in the semantic intelligence program.

The daemon reloads its shards live, without dropping a request or restarting.
Send it `SIGHUP`:

```sh
moe index refresh -shard-dir /path/to/shards   # update shards on disk
kill -HUP "$(pgrep -f 'moe serve.*shards')"    # daemon re-opens them
```

On `SIGHUP` the daemon opens a fresh view of the shard dir in the background and
atomically swaps it in only on success; in-flight queries keep running against
the previous generation, which is released once they drain (so the mmap is never
unmapped under an active read). A reload that fails to open keeps the current
generation serving. In `-mcp` mode the reload rebuilds the BM25 + symbol indices
and the dense arm too: it reuses the in-process embedder, so a reload over an
unchanged shard set reloads the persisted embedding sidecar instantly, while a
refreshed (changed) shard set re-embeds. This makes the steady-state loop a cron
job: `moe index refresh && kill -HUP <pid>`.

## Build-tag summary

- **Default build** (`go build ./cmd/moe`) — pure-Go engine, zero ML
  dependencies. All three modes work; `-embed onnx` degrades to lexical + symbol
  (and `-embed http` still works if an endpoint is configured).
- **Dense build** (`make build-dense`, i.e. `-tags onnx`) — pulls in the ONNX
  Runtime Go binding + tokenizer and embeds the model, producing
  `./moe-dense`. Needed for `-embed onnx`.
