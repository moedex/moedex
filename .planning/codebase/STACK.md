# Technology Stack

**Analysis Date:** 2026-08-24

## Languages

**Primary:**
- Go 1.26 — all engine, daemon, and CLI code. Library packages live under `internal/` (27 packages), executables under `cmd/` (8 binaries). Module name is `moedex` (`go.mod`), so internal imports are `moedex/internal/...`.

**Secondary:**
- Bash — operational scripts in `scripts/`: `install-macos.sh` (idempotent macOS install + launchd bootstrap), `install-lsp-servers.sh` (language-server provisioning), `refresh-corpus.sh` (end-to-end corpus refresh pipeline), `bench-latency.sh`, `managed-refresh-test.sh`.
- Make — `Makefile` is the canonical task runner and gate definition (`make health`, `make verify`, `make parity`).
- Dockerfile / YAML / systemd unit / launchd plist — deployment descriptors in `Dockerfile`, `Dockerfile.dense`, `.gitlab-ci.yml`, `deploy/*.service`, `deploy/*.timer`, `deploy/*.plist`.

**Languages parsed by the engine (inputs, not implementation):**
- Symbol extraction is polyglot and hand-written in Go — `internal/symbol/build_multi.go` dispatches by extension: `.go`, `.cs`, `.ts`/`.tsx`, `.cfm`/`.cfc`, `.sql`. Per-language extractors are `internal/symbol/extract.go`, `extract_cs.go`, `extract_ts.go`, `extract_cf.go`, `extract_sql.go`.
- Framework-aware classification (C#/ASP.NET controllers, MassTransit events) lives in `internal/classify/classify.go`.

## Runtime

**Environment:**
- Go 1.26 toolchain. `go.mod` declares `go 1.26`; CI and both Docker images pin `GOTOOLCHAIN=local` so no toolchain is auto-downloaded.
- Unix-only for the serving path. `internal/diskstore/diskstore.go:767` and `internal/graph/diskgraph/diskgraph.go:639` call `syscall.Mmap` directly with no Windows shim; `docs/WINDOWS-SUPPORT.md` records this as the single hard compile blocker for `GOOS=windows`.
- Only `internal/snapshot` carries a portability split: `lock_unix.go` (`darwin || dragonfly || freebsd || linux || netbsd || openbsd`) vs `lock_other.go`.

**Package Manager:**
- Go modules.
- Lockfile: present — `go.sum` (54 lines).
- One `replace` directive in `go.mod`: `github.com/sugarme/tokenizer` → `github.com/clems4ever/tokenizer v0.0.0-20250926133620-9ddc80533c43`. Module download therefore needs network access to github.com at build time (noted in `Dockerfile`).
- Large model assets are Git LFS-tracked: `.gitattributes` routes `internal/embed/onnxmodel/*` through LFS (`model.onnx` ~82 MB, `tokenizer.json` ~3.5 MB).

## Frameworks

**Core:**
- None. The default build is pure Go standard library — `net/http` serves the HTTP API directly via `http.ServeMux` (`cmd/moedex-serve/main.go:501-725`); there is no web framework, ORM, or DI container.
- `github.com/modelcontextprotocol/go-sdk` v1.7.0 — the official MCP SDK, the only third-party runtime dependency in the default build. Used in `internal/mcp/sdkserver.go` for tool registration, protocol negotiation, and the stateless Streamable HTTP transport exposed by `internal/mcp/http.go`.

**Testing:**
- Go stdlib `testing` only. No assertion library, no mocking framework, no `testify`.
- `github.com/google/jsonschema-go` v0.4.3 — direct `require` in `go.mod` but imported only from `internal/mcp/contract_test.go:14` (MCP tool-schema contract assertions). `go mod why` reports the main module does not need it outside tests.

**Build/Dev:**
- GNU Make — `Makefile` defines the gates. `make health` = build + vet + full unit suite; `make verify` = health + roundtrip + full-corpus parity (the DoD master gate).
- Docker — `Dockerfile` (default, `CGO_ENABLED=0`, `golang:1.26-bookworm` builder → `alpine:3.20` runtime) and `Dockerfile.dense` (`CGO_ENABLED=1`, ONNX Runtime bundled → `debian:12-slim` runtime, musl cannot host `libonnxruntime.so`).
- GitLab CI — `.gitlab-ci.yml`, three stages (`gate`, `parity`, `images`).

## Key Dependencies

**Critical (default build):**
- `github.com/modelcontextprotocol/go-sdk` v1.7.0 — MCP server protocol. Compiled into `moedex-serve` and `moedex-mcp`. See `internal/mcp/sdkserver.go`, `internal/mcp/contracts.go`.

**Build-tag-gated (NOT in the default build):**
- `github.com/yalue/onnxruntime_go` v1.21.0 — in-process dense embedder. Pulled in only by `//go:build onnx` files: `internal/embed/onnx.go`, `internal/embed/onnx_test.go`. Requires the ONNX Runtime shared library at run time via `ONNXRUNTIME_LIB_PATH` (resolution logic in `internal/embed/runtimepath.go`).
- `github.com/sugarme/tokenizer` v0.3.0 (replaced by the `clems4ever` fork) — HuggingFace tokenizer for the ONNX arm; imported from `internal/embed/onnx.go` (`tokenizer` + `tokenizer/pretrained`). Drags in `emirpasic/gods`, `mitchellh/colorstring`, `patrickmn/go-cache`, `rivo/uniseg`, `schollz/progressbar/v2`, `sugarme/regexpset`.

**Indirect (transitively required, per `go.mod`):**
- `github.com/segmentio/asm` v1.1.3, `github.com/segmentio/encoding` v0.5.4, `github.com/yosida95/uritemplate/v3` v3.0.2, `golang.org/x/oauth2` v0.35.0, `golang.org/x/sync` v0.20.0, `golang.org/x/sys` v0.41.0, `golang.org/x/text` v0.25.0, `golang.org/x/time` v0.15.0, `gopkg.in/yaml.v3` v3.0.1 — all reached through the MCP SDK / tokenizer, not imported by moedex code.

**Invariant:** the default build is pure Go standard library plus the MCP SDK, with zero ML/runtime deps. `CLAUDE.md` records this as a do-not-break rule: only the `onnx` tag may pull the embedder modules, only `moedex_simd` may use `archsimd` (which ships with the toolchain and is not a module dependency).

## Build Tags

| Tag | Build command | Adds | Run-time requirement |
|---|---|---|---|
| _(none)_ | `make build` | lexical + path + symbol retrieval, MCP, HTTP daemon | none |
| `onnx` | `make build-dense` | in-process ONNX embedder, semantic graph edges (9 files) | ONNX Runtime shared lib at `ONNXRUNTIME_LIB_PATH` |
| `lsp` | `make build-lsp` | LSP-precise navigation arm (28 files, mostly `internal/navigate/`) | language servers on `PATH` |
| `moedex_simd` | `make build-simd` | AVX2 set-ops kernel, `internal/setops/setops_simd_amd64.go` | amd64 + AVX2, `GOEXPERIMENT=simd` |

`internal/navigate/registry.go` deliberately carries no build tag so language routing compiles in both arms. Fallback files use the negated tags: `internal/embed/onnx_disabled.go` (`!onnx`), `internal/navigate/lsp_disabled.go` (`!lsp`), `cmd/moedex-serve/nav_stub.go`, `internal/setops` (`!(moedex_simd && amd64 && goexperiment.simd)`).

## Configuration

**Environment:**
- Configuration is flags-first with env fallback and an optional `KEY=VALUE` settings file. Precedence in `cmd/moedex-serve`: **flag > `-config` file > env > default** (`cmd/moedex-serve/config.go`).
- The `-config` file format is systemd `EnvironmentFile` syntax (no `export`, no shell expansion). Template: `deploy/moedex-serve.env.example`.
- Key configs: `MOEDEX_SHARD_DIR` or `MOEDEX_INDEX_DIR` (mutually exclusive), `MOEDEX_CORPUS`, `MOEDEX_CAS_DIR`, `MOEDEX_HTTP_ADDR`, `MOEDEX_MCP_HTTP_ADDR`, `MOEDEX_AUTH_TOKEN`, `MOEDEX_TLS_CERT`/`MOEDEX_TLS_KEY`, `MOEDEX_SEARCH_MAX_CONCURRENCY`, `MOEDEX_MCP_MAX_CONCURRENCY`. Full inventory in `INTEGRATIONS.md`.
- A `.env` file is not used; secrets live in `/etc/moedex/moedex-serve.env` (mode `0640`, `root:moedex`) on Linux or a `0600` token file at `~/.moedex-index/auth-token` on macOS.

**Build:**
- `Makefile` — canonical builds inject a deterministic worktree digest: `SOURCE_DIGEST = $(shell go run ./internal/version/buildmeta)` fed into `-ldflags "-X moedex/internal/version.SourceDigest=..."`. `make install` refuses a dirty worktree (`require-clean`) and deletes stale `GOPATH/bin` shadows so `PATH` cannot resolve an old binary — a guardrail added after a real index-loss incident (`internal/version/version.go`).
- `internal/version/version.go` also reads `runtime/debug.ReadBuildInfo()` VCS stamps; every binary answers `-version`.
- `.dockerignore` keeps the build context to `go.mod`/`go.sum` + Go source.
- `.gitignore` excludes built `cmd/*`-named binaries dropped in the repo root, `.parity-work*`, `test.log`, `.bench/`, and `.claude/worktrees/`.

## Platform Requirements

**Development:**
- Go 1.26, Git.
- `rg` (ripgrep) on `PATH` for the parity tests in `internal/search/parity_test.go` and `internal/parity` (they skip without it).
- Optional Zoekt differential oracle: `make setup` runs `go install github.com/sourcegraph/zoekt/cmd/{zoekt-index,zoekt}@latest`.
- Optional real corpus at `$MOEDEX_CORPUS` (default `$HOME/.moedex-managed`) for `make parity` / `make verify`. Scratch goes to repo-local `.parity-work/`.
- Optional language servers for the `lsp` arm via `make setup-lsp`, which needs `npm`, `brew`, `go`, and the .NET SDK.

**Production:**
- Single-node, self-hosted. Three shapes documented in `deploy/README.md`:
  - **macOS** — `scripts/install-macos.sh` installs binaries to `~/.local/bin` and bootstraps launchd agents `com.moedex.serve` (warm daemon, `-mcp-http 127.0.0.1:8081`) and `com.moedex.refresh` (daily 14:10 local).
  - **Linux systemd** — `deploy/moedex-serve.service` plus one of `moedex-sync.{service,timer}` (hourly pull + reindex + reload) or `moedex-refresh.{service,timer}` (reindex only).
  - **Docker** — `docker build -t moedex-serve .`; publish to host loopback only, mount shards `:ro` for `-http` and `:rw` for `-mcp`/dense (embedding cache).
- Sizing (measured on a ~5.2 GB / 484-repo corpus, `deploy/README.md`): serve `-http` ~3 GB RSS (comfortable on 8 GB); serve `-mcp` + dense ~4 GB heap (want ≥16 GB); build/refresh ~10 GB peak (build on a ≥24–32 GB host). Servable shard dir is 2.6 GB without dense, 5.4 GB with.
- Windows is not supported today; `docs/WINDOWS-SUPPORT.md` scopes WSL2 as phase 0.

---

*Stack analysis: 2026-08-24*
