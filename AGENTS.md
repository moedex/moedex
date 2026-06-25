# Repository Guidelines

## Project Structure & Module Organization

`moedex` is a Go 1.26 module for single-node code search and agent context retrieval. Executables live in `cmd/`, including `cmd/moedex`, `cmd/moedex-index`, `cmd/moedex-serve`, `cmd/moedex-mcp`, `cmd/moedex-parity`, and `cmd/moedex-corpus`. Library code is under `internal/`, grouped by responsibility: indexing in `internal/index`, query reduction in `internal/query`, retrieval in `internal/search`, ranking in `internal/rank`, serving in `internal/server`, MCP support in `internal/mcp`, and persistence in `internal/diskstore` and `internal/blobstore`. Tests sit beside implementation files as `*_test.go`. Design background belongs in `ARCHITECTURE.md`, `docs/adr/`, and `research/`; deployment examples and systemd units are in `deploy/`.

## Build, Test, and Development Commands

- `make build`: runs `go build ./...`.
- `make vet`: runs `go vet ./...`.
- `make test`: runs `go test ./...` and writes `test.log`.
- `make health`: build, vet, and full unit suite.
- `make roundtrip`: targeted persistence/parity round-trip tests.
- `make parity MOEDEX_CORPUS=/path/to/repos`: full-corpus parity gate against ripgrep/gold oracles.
- `make verify`: master gate: health, roundtrip, and parity.
- `make build-dense` / `make test-dense`: ONNX-tagged dense retrieval build and tests.

Use package-scoped commands while iterating, for example `go test ./internal/search -run TestName -count=1`.

## Coding Style & Naming Conventions

Format Go code with `gofmt`; keep imports managed by `go fmt`/`go test`. Use idiomatic Go names: short receiver names, `MixedCaps` exported identifiers, and lower-case package names without underscores. Keep command packages thin and put reusable behavior in `internal/`. Prefer pure-Go defaults; optional ONNX and SIMD paths must remain behind build tags documented in the Makefile.

## Testing Guidelines

Add or update colocated `*_test.go` files for behavior changes. Favor deterministic unit tests and parity tests for search behavior, especially where ripgrep-compatible matching is expected. Use `go test ./...` before broad changes and narrower package tests during development. Environment-gated skips are acceptable, but unexpected skips should be investigated because `make test` highlights them in `test.log`.

## Commit & Pull Request Guidelines

Recent history uses short imperative commits, often Conventional Commit prefixes such as `fix(corpus): ...`, `chore: ...`, and `docs: ...`. Keep commits focused and mention the affected package when useful. Pull requests should describe behavior changes, list validation commands run, link relevant issues or ADRs, and include screenshots only for user-visible docs or deployment UI changes.

## Security & Configuration Tips

Do not commit local corpora, generated shard data, dense binaries, or machine-specific environment files. Use `deploy/moedex-serve.env.example` as the template for service configuration. Treat `MOEDEX_CORPUS`, `ONNXRUNTIME_LIB_PATH`, and other local paths as environment-specific.
