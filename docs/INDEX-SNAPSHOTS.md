# Atomic index snapshots

An index snapshot is an immutable directory selected by one small atomic pointer:

```text
INDEX_DIR/
  CURRENT
  snapshots/<id>/
    snapshot.json
    serve/...
  .staging/...
```

`snapshot.json` is written last. It records the generation and parent, producer,
corpus fingerprint, capabilities, component formats, and the size and SHA-256 of
every artifact. Publication fsyncs staged files, renames the complete directory
into `snapshots/`, and atomically replaces `CURRENT`. A failed build cannot expose
a mixture of rank, graph, cluster, and dense generations.

The first compatibility step copies an existing servable shard directory into a
snapshot without sharing mutable hard links:

```sh
moedex-index snapshot-migrate \
  -index-dir "$HOME/.moedex-index-next" \
  -shard-dir "$HOME/.moedex-index/shards-managed"

moedex-index snapshot-list -index-dir "$HOME/.moedex-index-next"
moedex-index snapshot-inspect -index-dir "$HOME/.moedex-index-next"
moedex-serve -index-dir "$HOME/.moedex-index-next" -mcp-http 127.0.0.1:8081
```

The direct path builds shards, rank data, graph data, clusters, and optionally
dense vectors inside private staging, and treats any requested component failure
as publication-blocking:

```sh
moedex-index snapshot-build \
  -index-dir "$HOME/.moedex-index-next" \
  -corpus "$HOME/.moedex-managed" \
  -dense
```

Rollback validates the selected generation and changes only `CURRENT`:

```sh
moedex-index snapshot-rollback -index-dir /path/to/index -id 20260820T120000Z-4
```

The transitional snapshot keeps the serving tree under `serve/` so current
readers can consume it unchanged. `snapshot-build` writes that complete tree
directly; the later layout split can reorganize components without weakening the
single publication boundary. `-build-embeddings` refuses `-index-dir` because
published snapshots are immutable; use `snapshot-build -dense` instead.

ONNX corpus throughput is machine-specific. Tune with the production batch shape:

```sh
ONNXRUNTIME_LIB_PATH=/path/to/libonnxruntime \
go test -tags onnx ./internal/embed \
  -run '^$' -bench BenchmarkONNXEmbedderThreadCounts -benchtime=3x
```

Apply the reviewed result with `MOEDEX_ONNX_INTRA_OP_THREADS` (or
`-onnx-intra-op-threads`). Leave inter-op at zero or one unless measurement shows
the model benefits from parallel graph-node execution.
