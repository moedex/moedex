# Moe

Moe is a single-node code-search engine and agent context service built around
positional trigrams, content-addressed storage, hybrid ranking, and ripgrep-parity
correctness. One `moe` executable owns the interactive shell, automation commands,
indexing, serving, MCP, corpus management, navigation, and diagnostics.

The retrieval engine remains pure Go by default. Optional build tags add in-process
ONNX embeddings, LSP-backed navigation, or an experimental SIMD set-operations
kernel.

## Requirements

- Go 1.26
- Git
- [ripgrep](https://github.com/BurntSushi/ripgrep) for parity tests
- Zoekt only for the optional differential oracle (`make setup` installs it)

## Build and install

```sh
make build
make install                    # installs to ~/.local/bin by default
```

Override the destination with `make install BINDIR=/path/to/bin`. Installation
writes one real `moe` executable and compatibility symlinks for the former binary
names. It refuses to overwrite an unfamiliar target; use
`ADOPT_LEGACY_BINARIES=1` only after confirming that the exact targets should be
adopted.

## Quick start

Search any Git repository without creating a persistent index:

```sh
go run ./cmd/moedex search --repo /path/to/repository 'SearchContext'
```

For a persistent corpus, publish an index and start the warm daemon:

```sh
moedex index build --corpus /path/to/corpus --shard-dir /path/to/shards
moedex serve --shard-dir /path/to/shards --http 127.0.0.1:8080
curl 'http://127.0.0.1:8080/search?q=SearchContext'
```

Set `MOEDEX_INDEX_DIR` for an immutable snapshot root or `MOEDEX_SHARD_DIR` for a
servable shard directory. `moedex search PATTERN`, bare interactive `moe`, and
`moedex search --tui` then use that published index by default:

```sh
export MOEDEX_SHARD_DIR=/path/to/shards
moedex search SearchContext        # deterministic one-shot output
moe                              # interactive TUI when stdin/stdout are TTYs
```

The TUI debounces queries, cancels stale searches, renders token-budgeted context,
shows graph relationships when the graph sidecar is present, and copies the
selected Markdown block with OSC52 on `ctrl+y`. Bare non-TTY invocation prints
help instead of trying to open an interactive program.

Promote an existing shard directory into an immutable snapshot and serve its
`CURRENT` generation:

```sh
moedex index snapshot migrate --index-dir /path/to/index --shard-dir /path/to/shards
moedex serve --index-dir /path/to/index --mcp-http 127.0.0.1:8081
```

`snapshot list`, `snapshot inspect`, and `snapshot rollback` inspect or switch
complete generations without rewriting artifacts. `snapshot build` stages and
publishes a new generation atomically; an ONNX-tagged build accepts `-dense`.

Network listeners fail closed: a tokenless non-loopback `--http` or `--mcp-http`
bind is rejected. Configure `MOEDEX_AUTH_TOKEN`, keep the listener on loopback,
or pass `--allow-insecure` as an explicit local policy exception. See the
[serving guide](internal/app/servecmd/README.md) for endpoints, TLS, authentication,
reload behavior, and deployment hardening.

## Command tree

| Command | Purpose |
|---|---|
| `moedex search [PATTERN]` | Search the published index, a one-off repository, or open the TUI |
| `moedex index build\|check\|refresh` | Build and maintain servable shard indexes |
| `moedex index cas …` | Build, refresh, export, and compact the content-addressable store |
| `moedex index snapshot …` | Build, inspect, migrate, switch, and roll back immutable generations |
| `moedex graph build\|audit` | Build and audit graph sidecars |
| `moedex corpus …` | Initialize, synchronize, diagnose, and size managed corpora |
| `moedex serve` | Serve warm retrieval and MCP endpoints |
| `moe mcp` | Serve MCP over stdio; `--repo` selects the single-repository mode |
| `moedex nav def\|refs\|impl` | Run the optional LSP navigation arm |
| `moedex parity` | Run the full-corpus correctness gate |
| `moedex doctor` | Diagnose corpus, index, and daemon health |
| `moedex config` | Show effective settings and provenance; add `--diff` or `--json` |
| `moedex version` | Print the build identity and compiled dense capability |

Run `moe completion --help` for shell completion generation and `moe man` for a
roff man page. Global `--config`, `--json`, and `--no-color` flags may appear
before or after semantic commands.

The old `moedex`, `moedex-index`, `moedex-serve`, `moedex-mcp`, `moedex-corpus`,
`moedex-parity`, `moedex-nav`, and `scale` names remain direct in-process
compatibility symlinks for two releases. They print a deprecation warning and
preserve their legacy arguments; new automation should use the semantic tree.

## Configuration

Existing public `MOEDEX_*` names remain stable. Moe never discovers a config file
implicitly; load one explicitly with `--config FILE`. Precedence is command flag,
explicit file, ambient environment, then built-in default. The file is validated
as typed `KEY=VALUE` input, unknown `MOEDEX_*` keys are fatal, and secrets are
redacted from `moedex config` output. See [docs/configuration.md](docs/configuration.md)
for the registry and file format.

Long-running commands emit human progress on stderr. Add `--json` for versioned
NDJSON progress on stdout, leaving diagnostics on stderr.

## Validation

```sh
make health       # format/import fences, build, vet, and full unit suite
make tagged-check # ONNX, LSP, and SIMD compile/test gates
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

The lexical, path, and symbol retrieval paths remain available without optional
components.

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) describes implementation, data flow, on-disk
  formats, and deliberate deferrals.
- [docs/adr/](docs/adr) records architectural decisions and evidence.
- [deploy/README.md](deploy/README.md) covers macOS, Docker, and systemd.
- [research/](research) contains exploratory inputs, not statements of current
  behavior.
