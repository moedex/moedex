# moedex-serve

The warm retrieval daemon. It memory-maps a directory of prebuilt shards **once**
and answers queries with zero cold-start, in one of three modes:

| Mode | Flag | Surface | What it serves |
|------|------|---------|----------------|
| Retrieval daemon | `-http :8080` | HTTP JSON (`/search`, `/stats`, `/healthz`) | line-granular literal/regex matches, parity-proven against ripgrep |
| One-shot query | `-q PATTERN` | stdout (`repo/relpath:line`) | a single retrieval query, for validation/scripting |
| Ranked agent context | `-mcp` | MCP over stdio (`search_context` tool) | ranked, deduplicated, token-budgeted context blocks |

`-shard-dir` is always required (or `MOEDEX_SHARD_DIR`), and you must pick exactly
one mode. With none of `-mcp`/`-http`/`-q`, the process exits with usage on stderr.

## Quick start

```sh
# Retrieval daemon
moedex-serve -shard-dir /path/to/shards -http :8080

# One-shot literal query (add -regex for a pattern)
moedex-serve -shard-dir /path/to/shards -q "func main" -regex

# Ranked agent context over MCP (lexical + symbol arm, no extra deps)
moedex-serve -shard-dir /path/to/shards -mcp
```

On the two warm modes the daemon prints a boot line to stderr, e.g.
`mmap'd 12 shards, 480123 blobs in 8ms`.

## What a shard dir is

A shard dir holds one or more `*.idx` files plus a `manifest.json` freshness
sidecar. Produce one with the offline indexer `moedex-index`:

```sh
moedex-index build -corpus /path/to/corpus-root -shard-dir /path/to/shards
```

It discovers every git repo under the corpus root, ingests them into a sequence
of content-sized shards (`shard-0000.idx`, `shard-0001.idx`, … — flushed at
`DefaultShardBytes` ≈ 150 MB of indexed content each), and writes the manifest
alongside. The result is directly servable as `-shard-dir`. See
[../moedex-index](../moedex-index) for `check`/`refresh`.

`server.Open(dir)` globs `*.idx` in sorted filename order and `mmap`s each via
`diskstore.LoadMmap`, holding the mappings for the process lifetime — so postings
never enter the Go heap and queries pay only decode-on-touch. After a `Close` the
corpus must not be queried (the memory is unmapped); `Close` is idempotent.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-shard-dir` | `$MOEDEX_SHARD_DIR` | directory of prebuilt `*.idx` shards (**required**) |
| `-http` | _(off)_ | serve the retrieval HTTP API on this address (e.g. `:8080`) |
| `-mcp` | `false` | serve ranked agent context over MCP (stdio) |
| `-q` | _(off)_ | one-shot retrieval query |
| `-regex` | `false` | treat `-q` / the `/search` query as a regular expression (default: literal) |
| `-limit` | `0` | cap matches printed/returned (`0` = no cap) |
| `-top-k` | `20` | default ranked results per MCP query |
| `-embed` | `auto` | dense embedder for `-mcp`: `auto`\|`onnx`\|`http`\|`none` |
| `-onnx-runtime` | `$ONNXRUNTIME_LIB_PATH` | path to the ONNX Runtime shared library (in-process embedder; requires an `-tags onnx` build) |

## Retrieval daemon (`-http`)

A minimal JSON API backed by `server.Corpus`. It runs until `SIGINT`/`SIGTERM`,
then shuts down gracefully (5 s drain); `SIGHUP` hot-reloads the shards (see
[Hot reload](#hot-reload-sighup)).

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

## One-shot query (`-q`)

Runs a single query against the warm corpus and prints `repo/relpath:line`, one
per line, with a `N match(es)` summary on stderr. Add `-regex` for a pattern;
`-limit N` truncates the printed list. Useful for scripted spot-checks and parity
validation.

## Ranked agent context (`-mcp`)

`-mcp` serves the `search_context` MCP tool over stdio, backed by
`server.RankCorpus` — the ranked, corpus-wide surface.

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
  "arguments": {"query": "csr validation", "token_budget": 4000, "top_k": 10}
}
```

Only `query` is required; `token_budget` and `top_k` are optional (the latter
defaults to `-top-k`). An empty query is reported as a tool-level error rather
than a protocol error.

The MCP server applies hardening defaults: a 30 s per-call timeout, 8-way handler
concurrency, a 1 MiB cap on a single JSON-RPC message, and an 8 KiB cap on the
query string. Requests are decoded by a single reader, handled by a bounded
worker pool, and written by a single serialized writer, so responses may complete
out of order (legal under JSON-RPC — each reply carries its request id) but output
framing is never interleaved.

## The dense arm

The dense (embedding) arm is **`-mcp` only** and **optional**. When it is off,
ranking is pure lexical (BM25) + symbol arm with zero external dependencies. The
`-embed` flag selects the embedder:

| `-embed` | Behavior |
|----------|----------|
| `auto` (default) | `onnx` if `-onnx-runtime`/`ONNXRUNTIME_LIB_PATH` is set, else `http` if `MOEDEX_EMBED_URL` is set, else `none` |
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
make build-dense          # go build -tags onnx -o moedex-serve-dense ./cmd/moedex-serve
```

At run time it needs the ONNX Runtime **shared library** (the model weights are
bundled, the runtime is not). Install it and point the daemon at it:

```sh
brew install onnxruntime  # provides libonnxruntime.dylib

./moedex-serve-dense -shard-dir /path/to/shards -mcp -embed onnx \
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
ingest. The `moedex-index` command drives shard-level partial re-indexing off it:

```sh
moedex-index check   -shard-dir /path/to/shards   # dry run: changed/added/removed repos
moedex-index refresh -shard-dir /path/to/shards   # rebuild affected shards, swap in place
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

The daemon reloads its shards live, without dropping a request or restarting.
Send it `SIGHUP`:

```sh
moedex-index refresh -shard-dir /path/to/shards   # update shards on disk
kill -HUP "$(pgrep -f 'moedex-serve.*shards')"     # daemon re-opens them
```

On `SIGHUP` the daemon opens a fresh view of the shard dir in the background and
atomically swaps it in only on success; in-flight queries keep running against
the previous generation, which is released once they drain (so the mmap is never
unmapped under an active read). A reload that fails to open keeps the current
generation serving. In `-mcp` mode the reload rebuilds the BM25 + symbol indices
and the dense arm too: it reuses the in-process embedder, so a reload over an
unchanged shard set reloads the persisted embedding sidecar instantly, while a
refreshed (changed) shard set re-embeds. This makes the steady-state loop a cron
job: `moedex-index refresh && kill -HUP <pid>`.

## Build-tag summary

- **Default build** (`go build ./cmd/moedex-serve`) — pure-Go, zero ML
  dependencies. All three modes work; `-embed onnx` degrades to lexical + symbol
  (and `-embed http` still works if an endpoint is configured).
- **Dense build** (`make build-dense`, i.e. `-tags onnx`) — pulls in the ONNX
  Runtime Go binding + tokenizer and embeds the model, producing
  `./moedex-serve-dense`. Needed for `-embed onnx`.
