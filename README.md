# moedex

moedex is a single-node code-search engine and agent context service built around
positional trigrams, content-addressed storage, hybrid ranking, and ripgrep-parity
correctness.

The default build is pure Go. Optional build tags add in-process ONNX embeddings,
LSP-backed navigation, or an experimental SIMD set-operations kernel without
changing the default runtime.

## Requirements

- Go 1.26
- Git
- [ripgrep](https://github.com/BurntSushi/ripgrep) for parity tests
- Zoekt only for the optional differential oracle (`make setup` installs it)

## Build

```sh
make build
```

To install the operational binaries into `~/.local/bin`:

```sh
make install
```

Override the destination with `make install BINDIR=/path/to/bin`.

## Quick start

Run a one-shot literal search against any Git repository:

```sh
go run ./cmd/moedex -repo /path/to/repository 'SearchContext'
```

Add `-regex` to interpret the pattern as a regular expression. The command
indexes the repository in memory, prints each match as `path:line`, and exits.

For a persistent corpus, build a shard directory and start the warm daemon:

```sh
go run ./cmd/moedex-index build \
  -corpus /path/to/corpus \
  -shard-dir /path/to/shards

go run ./cmd/moedex-serve \
  -shard-dir /path/to/shards \
  -http 127.0.0.1:8080

curl 'http://127.0.0.1:8080/search?q=SearchContext'
```

An existing servable directory can be promoted into an immutable atomic index
snapshot, then served through its `CURRENT` pointer:

```sh
moedex-index snapshot-migrate -index-dir /path/to/index -shard-dir /path/to/shards
moedex-serve -index-dir /path/to/index -mcp-http 127.0.0.1:8081
```

`snapshot-list`, `snapshot-inspect`, and `snapshot-rollback` inspect or switch
complete generations without rewriting their artifacts.

New generations can also be built directly in private staging and published in
one switch with `moedex-index snapshot-build -corpus ROOT -index-dir DIR`; add
`-dense` to an ONNX-tagged indexer to include vectors before publication.

The daemon also serves ranked, deduplicated, token-budgeted context through MCP
over stdio (`-mcp`) or Streamable HTTP (`-mcp-http 127.0.0.1:8081`). See the
[`moedex-serve` guide](cmd/moedex-serve/README.md) for its modes, flags, HTTP
responses, authentication, and reload behavior.

## Commands

| Command | Purpose |
|---|---|
| `moedex` | Index one repository in memory and run a literal or regex search |
| `moedex-index` | Build, inspect, refresh, export, and compact servable indexes and the corpus-wide CAS |
| `moedex-serve` | Serve warm retrieval over HTTP, one-shot queries, or ranked agent context over MCP |
| `moedex-mcp` | Serve ranked context for one repository over MCP/stdio |
| `moedex-corpus` | Acquire and refresh a managed corpus |
| `moedex-parity` | Run the full-corpus correctness gate against ripgrep, gold, and optionally Zoekt |

## Validation

```sh
make health       # build, vet, and the full unit suite
make roundtrip    # persistence and parity round trips
make parity       # full-corpus parity; set MOEDEX_CORPUS to the corpus root
make verify       # health + roundtrip + parity
```

Package-scoped tests are faster while iterating:

```sh
go test ./internal/search -run TestName -count=1
```

## Optional builds

| Feature | Command | Runtime requirement |
|---|---|---|
| In-process dense retrieval | `make build-dense` | ONNX Runtime shared library |
| LSP-precise navigation | `make build-lsp` | Language servers on `PATH`; use `make setup-lsp` |
| Experimental AVX2 set operations | `make build-simd` | amd64 with `GOEXPERIMENT=simd` |

The lexical, path, and symbol retrieval paths remain available without any of
these optional components.

## Documentation

- [`ARCHITECTURE.md`](ARCHITECTURE.md) describes the implementation, data flow,
  on-disk formats, and deliberate deferrals.
- [`docs/adr/`](docs/adr) records architectural decisions and their evidence.
- [`docs/plans/`](docs/plans) contains implementation plans for proposed work.
- [`research/`](research) contains exploratory design notes; these are inputs to
  decisions, not statements of current behavior.
- [`deploy/README.md`](deploy/README.md) covers macOS, Docker, and systemd
  deployment.
