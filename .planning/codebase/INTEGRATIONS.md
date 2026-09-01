# External Integrations

**Analysis Date:** 2026-08-24

Every external dependency in this codebase is an **out-of-process CLI, local HTTP endpoint, or dynamically loaded shared library** — never a linked cloud SDK. There is no SaaS API client, no cloud provider SDK, and no database driver anywhere in the tree.

## APIs & External Services

**Source control / corpus acquisition (subprocess only):**
- **GitLab — `gitlab.tcdevops.com`** — the corpus source. Project enumeration goes through the `glab` CLI, never a linked API client.
  - SDK/Client: none. `internal/corpus/enumerate.go:114` shells `glab api --hostname <host> --paginate ...`; the response is parsed by `ParseProjects` (`internal/corpus/enumerate.go:53`).
  - Auth: delegated entirely to `glab`. `internal/corpus/doctor.go:94` runs `glab auth status --hostname <host>`; the remediation it prints is `glab auth login --hostname gitlab.tcdevops.com`. `GITLAB_TOKEN` is the non-interactive alternative (glab reads it) — documented in `deploy/moedex-serve.env.example`.
  - Host scope is fixed: `internal/corpus/defaults.go` + `internal/corpus/config.go` scope acquisition to `gitlab.tcdevops.com` only. The curated group allowlist ships as `internal/corpus/default_groups.txt`.
  - **Boundary invariant:** `internal/corpus` is the only package that shells out to `glab`/`git` for acquisition, and it is never imported by the engine or the daemon. All external process invocation funnels through `internal/corpus/runner.go:90` (`Runner.Run` → `exec.CommandContext`), which tests replace with a fake.
- **Git CLI** — read-only working-tree ingestion and build stamping.
  - `internal/ingest/ingest.go:58` — `git -C <dir> rev-parse HEAD`
  - `internal/ingest/ingest.go:81` — `git -C <dir> ls-files -s -z` (the sole file-discovery mechanism; content identity is the git blob SHA it reports)
  - `internal/version/sourcehash/sourcehash.go:99` — `git` invocations for the deterministic dirty-worktree digest
  - Clones are `--depth 1` single-branch; LFS blobs are skipped (binary, ignored by the indexer anyway).

**Embeddings (optional dense arm):**
- **OpenAI-compatible embeddings server** (ollama, llama.cpp-server, or any `/embeddings` endpoint) — `internal/embed/embed.go`.
  - Client: hand-rolled `HTTPEmbedder` on stdlib `net/http` (`internal/embed/embed.go:79-140`). Issues `POST {baseURL}/embeddings` with `{"model": ..., "input": [...]}` and parses `{"data":[{"embedding":[...]}]}`. 60s per-request timeout (`defaultEmbedTimeout`); response body is size-capped via `io.LimitReader`.
  - Config: `MOEDEX_EMBED_URL` + `MOEDEX_EMBED_MODEL`. Both must be set to light this arm up; it is `-mcp` only, never used by `-http`.
  - Auth: none — the endpoint is assumed local/loopback.
- **ONNX Runtime (in-process alternative)** — `internal/embed/onnx.go`, `//go:build onnx` only.
  - Binding: `github.com/yalue/onnxruntime_go` v1.21.0, which `dlopen`s the shared library at run time.
  - Config: `ONNXRUNTIME_LIB_PATH` (resolution + probing in `internal/embed/runtimepath.go`); tuning via `MOEDEX_ONNX_INTRA_OP_THREADS` / `MOEDEX_ONNX_INTER_OP_THREADS`.
  - Model asset: `internal/embed/onnxmodel/model.onnx` (~82 MB, int8 st-codesearch-distilroberta) + `tokenizer.json`, Git LFS-tracked and embedded into the binary. Overridable via `MOEDEX_CODE_MODEL` / `MOEDEX_CODE_TOKENIZER` / `MOEDEX_CODE_DIM`.
  - `Dockerfile.dense` fetches `onnxruntime-linux-<arch>-${ONNXRUNTIME_VERSION}.tgz` from `https://github.com/microsoft/onnxruntime/releases/...` (default `1.27.0`; the binding needs ≥ 1.27.0).

**Language servers (optional navigation arm, `-tags lsp`):**
- Launched as external processes over LSP/stdio by `internal/navigate/lsp.go:321`. They are never linked or imported. The authoritative set is `defaultRegistry` in `internal/navigate/registry.go`:

  | Language | Command | Registry line |
  |---|---|---|
  | Go | `gopls` | `internal/navigate/registry.go:75` |
  | TypeScript/JS | `typescript-language-server` | `:81` |
  | Python | `pyright-langserver` | `:87` |
  | Rust | `rust-analyzer` | `:93` |
  | C/C++ | `clangd` | `:100` |
  | C# | `csharp-ls` | `:111` |
  | SQL | `sql-language-server` | `:129` |
  | CFML | `cflsp` | `:136` |
  | CSS/SCSS | `vscode-css-language-server` | `:155` |
  | HTML | `vscode-html-language-server` | `:161` |

  - Provisioning: `scripts/install-lsp-servers.sh` (idempotent; `--with-cfml` is opt-in because it builds external source). Verify with `moedex-index doctor`.
  - `LangSpec.ResolveEnv` computes per-server env at launch — notably `DOTNET_ROOT` for `csharp-ls` on Homebrew .NET installs.
  - Degradation is graceful: a missing server makes that language's `find_definition`/`find_references`/`find_implementations` return empty, never an error.
  - Offline sweep throttles: `MOEDEX_GRAPH_LSP_CONCURRENCY` (default `min(2*GOMAXPROCS, 32)`) and `MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND` (default 100). Debug via `MOEDEX_LSP_DEBUG`.

**Correctness oracles (test/CI only):**
- **ripgrep (`rg`)** — the parity ground truth. `internal/parity/oracle_ripgrep.go:78` shells `rg`. Tests skip when it is absent (`requireRipgrep` in `internal/search/parity_test.go`). CI installs it in the `health` job's `before_script`.
- **Zoekt** — optional differential oracle. `internal/parity/oracle_zoekt.go:56,102` shell `zoekt-index` and `zoekt -index_dir ... -l`. Installed by `make setup`.

**Host process management:**
- `launchctl print gui/<uid>/<label>` — `cmd/moedex-index/doctor.go:380`, so `doctor` can report launchd agent state on macOS. Label overridable via `MOEDEX_LAUNCHD_LABEL`.
- `cmd/moedex-index/doctor.go:235` runs `<binary> -version` on each installed moedex binary to detect PATH shadowing/skew.

## Data Storage

**Databases:**
- None. No SQL, no key-value store, no driver in `go.mod`. All persistence is custom on-disk formats written and read by moedex itself.

**File Storage:**
- Local filesystem only. Four purpose-built formats, all mmap-read so postings never enter the Go heap:
  - **Shard index** — `internal/diskstore/diskstore.go` (`Save` / `LoadMmap` / `LoadMmapDeduped`, `syscall.Mmap` at `:767`). A shard dir is `shard-NNNN.idx` files (~150 MB indexed content each) plus `manifest.json`.
  - **Content-addressable blob store (CAS)** — `internal/blobstore/`. Keyed by git blob SHA so identical content is stored once corpus-wide. Lifecycle commands: `cas-build`, `cas-refresh`, `cas-export -deduped`, `compact`.
  - **Graph sidecar** — `internal/graph/diskgraph/diskgraph.go` (`syscall.Mmap` at `:639`), plus `internal/graph/{candidates,cluster,manifest,httproute,verify}`.
  - **Immutable snapshots** — `internal/snapshot/`. A snapshot root holds numbered generations selected by a `CURRENT` pointer; `publish.go` does the atomic switch, `lock_unix.go` / `lock_other.go` provide the advisory lock. Managed via `moedex-index snapshot-build|snapshot-migrate|snapshot-list|snapshot-inspect|snapshot-rollback`.
- Atomicity everywhere is directory rename: a fresh dir is built beside the live one and renamed in, which is why the systemd refresh unit needs `ReadWritePaths` on the shard dir **and** its parent (`deploy/README.md`).
- Dense sidecar and embedding cache are written next to the shards, which is why `-mcp`/dense needs the shard mount `:rw` while `-http` can be `:ro`.

**Caching:**
- In-process only. `internal/mcp/sdkserver.go:29` holds a 5-minute `catalogCacheTTL` for the tool catalog. The embedding sidecar is fingerprint-keyed and content-hash incremental — `scripts/refresh-corpus.sh` re-embeds only chunks whose text changed. No Redis, no memcached.

## Authentication & Identity

**HTTP / MCP API:**
- Static bearer token, implemented in `cmd/moedex-serve/middleware.go:164-186`. When `MOEDEX_AUTH_TOKEN` (or `-auth-token`, which overrides it — precedence at `cmd/moedex-serve/main.go:121`) is set, `Authorization: Bearer <token>` is required. `/healthz` and `/metrics` stay open unconditionally (`openPaths`, `middleware.go:23`).
- No user identity, no sessions, no OAuth, no RBAC. One shared token per daemon.
- The daemon logs a `slog.Warn` when it binds without a token, and a louder warning on a tokenless non-loopback bind (`cmd/moedex-serve/main.go:483-485`).
- Optional TLS: `-tls-cert` / `-tls-key`, env `MOEDEX_TLS_CERT` / `MOEDEX_TLS_KEY` (`cmd/moedex-serve/main.go:80-81`). Otherwise the expectation is a loopback bind behind a trusted proxy.

**GitLab:**
- Fully delegated to `glab`'s own credential store, or `GITLAB_TOKEN` in the environment. moedex never stores, reads, or forwards GitLab credentials — `internal/corpus/doctor.go` only *inspects and guides*. Git clone/fetch additionally needs SSH access with the GitLab host key pre-seeded in `known_hosts` (the systemd unit keeps `HOME` read-only, so first-contact `accept-new` cannot write it).
- `doctor` reports authentication, VPN/API reachability, and Git transport as **three independent checks** — a valid token does not imply a connected VPN.

**Secret handling:**
- Linux: `/etc/moedex/moedex-serve.env`, mode `0640` `root:moedex`, loaded via systemd `EnvironmentFile=`. `deploy/README.md` recommends `LoadCredential=`/`systemd-creds` on hosts that support it.
- macOS: a `0600` token file at `~/.moedex-index/auth-token`. `deploy/com.moedex.serve.plist` wraps the daemon in `/bin/sh -c` that reads the file, exports `MOEDEX_AUTH_TOKEN`, and republishes it to the GUI session as `MOEDEX_TOKEN` via `launchctl setenv` — the secret is deliberately never written into the plist.
- Diagnostic output from external commands is scrubbed: `redactDiagnostic` in `internal/corpus/enumerate.go`.

## Monitoring & Observability

**Error Tracking:**
- None. No Sentry, no APM agent, no telemetry egress of any kind.

**Metrics:**
- `/metrics` on the `-http` and `-mcp-http` daemons, stdlib-only by design (`cmd/moedex-serve/obs.go:12-14`): `expvar` counters rendered as Prometheus text exposition v0.0.4 at scrape time. Explicitly **no** `prometheus/client_golang`.
- Published series: `moedex_http_requests_total` (by `2xx`/`4xx`/`5xx` class), `moedex_http_panics_total`, `moedex_reloads_total` (by `ok`/`fail`), `moedex_http_search_rejected_total`, `moedex_http_mcp_rejected_total` (`cmd/moedex-serve/obs.go:58-77`).
- `/stats` returns corpus/shard statistics and is auth-gated.

**Logs:**
- `log/slog` JSON handler to stderr, installed at `cmd/moedex-serve/main.go:455`. Structured access logging and panic recovery in `cmd/moedex-serve/middleware.go:73,102`.
- Collection is the platform's: `journalctl -u moedex-serve -f` on systemd; `StandardOutPath`/`StandardErrorPath` → `~/.moedex-index/moedex-serve.log` on launchd; `docker logs` in the container.

**Health:**
- `/healthz` returns 200 `ok`, always unauthenticated. The default image's `HEALTHCHECK` probes it with busybox `wget` every 30s.
- `moedex-corpus doctor` and `moedex-index doctor` are the read-only operational diagnostics; neither mutates anything.

## CI/CD & Deployment

**Hosting:**
- Self-hosted single node. No cloud platform, no orchestrator. See `deploy/README.md` for the macOS launchd, Linux systemd, and Docker paths.

**CI Pipeline:**
- GitLab CI (`.gitlab-ci.yml`), default image `golang:1.26`, runner tag `docker-image`, `GOTOOLCHAIN: local`, module cache keyed on `go.sum` at `$CI_PROJECT_DIR/.gocache`.
  - **`gate` / `health`** — `make health` on every push, MR, and web trigger. Installs `ripgrep` in `before_script` so the rg-backed parity unit tests actually run instead of skipping.
  - **`parity` / `parity`** — `make parity` on schedules, tags, and MRs touching `internal/query/**`, `internal/search/**`, or `internal/parity/**`. Requires a **self-hosted runner tagged `moedex-corpus`** with the corpus at `$MOEDEX_CORPUS`, `rg` on PATH, and disk for `.parity-work`. Publishes `PARITY-REPORT.md` as a 30-day artifact.
  - **`parity` / `graph-eval-private`** — `make graph-eval-private` on MRs/pushes touching `internal/graph`, `internal/contextwin`, `internal/mcp`, `internal/server`, `internal/eval`, `cmd/moedex-serve`, `cmd/moedex-index`, `Makefile`, or `.gitlab-ci.yml`. Reads mounted paths from project variables `CI_MOEDEX_GRAPH_EVAL_SHARDS` and `CI_MOEDEX_GRAPH_GOLD`, remapped to `MOEDEX_GRAPH_EVAL_SHARDS`/`MOEDEX_GRAPH_GOLD` inside the job only. `before_script` fails fast if either mount is missing.
  - **`images` / `build-images`** — manual, `allow_failure: true`. `docker:27` + `docker:27-dind`, pushes `$CI_REGISTRY_IMAGE:latest` and `:dense` to the GitLab container registry using `$CI_REGISTRY_USER`/`$CI_REGISTRY_PASSWORD`.

**Scheduled refresh:**
- Linux — enable exactly ONE of `moedex-sync.timer` (hourly; pulls from GitLab, re-indexes, reloads — needs network + glab + SSH) or `moedex-refresh.timer` (reindex only from an already-updated local corpus, no network). `moedex-sync.timer` uses `OnCalendar=hourly`, `RandomizedDelaySec=300`, `Persistent=true`.
- macOS — `com.moedex.refresh` launchd agent at 14:10 local, chosen because the TC VPN session times out after 12 hours and an operator is likelier to be connected then.
- Reload is SIGHUP → warm hot-swap with no dropped requests (`systemctl reload moedex-serve`, or `launchctl kill -HUP gui/$(id -u)/com.moedex.serve`). SIGINT/SIGTERM does a 5s graceful drain.

## Environment Configuration

**Serving (required):**
- `MOEDEX_SHARD_DIR` **or** `MOEDEX_INDEX_DIR` — mutually exclusive; one is required by `moedex-serve`.

**Serving (surface + security):**
- `MOEDEX_HTTP_ADDR`, `MOEDEX_MCP_HTTP_ADDR`, `MOEDEX_AUTH_TOKEN` (**secret**), `MOEDEX_TLS_CERT`, `MOEDEX_TLS_KEY`, `MOEDEX_SEARCH_MAX_CONCURRENCY` (default 8), `MOEDEX_MCP_MAX_CONCURRENCY`.

**Corpus lifecycle:**
- `MOEDEX_CORPUS` (default `$HOME/.moedex-managed`), `MOEDEX_CORPUS_ROOT`, `MOEDEX_CAS_DIR`, `GITLAB_TOKEN` (**secret**), and the binary-resolution overrides `MOEDEX_CORPUS_BIN`, `MOEDEX_INDEX_BIN`, `MOEDEX_SERVE_BIN`, `MOEDEX_TOOLS_DIR`, `MOEDEX_LAUNCHD_LABEL`, `MOEDEX_NOTE`.

**Dense arm:**
- `MOEDEX_EMBED`, `MOEDEX_EMBED_URL`, `MOEDEX_EMBED_MODEL`, `ONNXRUNTIME_LIB_PATH`, `MOEDEX_ONNX_INTRA_OP_THREADS`, `MOEDEX_ONNX_INTER_OP_THREADS`, `MOEDEX_CODE_MODEL`, `MOEDEX_CODE_TOKENIZER`, `MOEDEX_CODE_DIM`.

**Graph:**
- `MOEDEX_GRAPH_SIMILAR_TOP_K` (default 5), `MOEDEX_GRAPH_SIMILAR_THRESHOLD` (0.60), `MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT` (4096), `MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES` (2048), `MOEDEX_GRAPH_CLUSTER_MAX_NODES`, `MOEDEX_GRAPH_LSP_CONCURRENCY`, `MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND`.

**Privacy-aware indexing (ADR 0021):**
- `MOEDEX_PRIVACY_CORPUS`, `MOEDEX_PRIVACY_CAS_MANIFEST`, `MOEDEX_PRIVACY_SERVED_MANIFEST`.

**Test / bench / CI gates (never set in production):**
- `MOEDEX_EVAL_CORPUS`, `MOEDEX_GRAPH_EVAL_SHARDS`, `MOEDEX_GRAPH_GOLD`, `MOEDEX_BENCH_SHARDS`, `MOEDEX_VERIFY_CONTENT`, `MOEDEX_MMAP`, `MOEDEX_SELECTIVE`, `MOEDEX_TEST_NEW`, `MOEDEX_TEST_OVERRIDE`, `MOEDEX_CAS_PARITY_CORPUS`, `MOEDEX_CAS_PARITY_MAXREPOS`, `MOEDEX_CAS_PARITY_SHARDBYTES`, `MOEDEX_COMPACT_PARITY_CORPUS`, `MOEDEX_COMPACT_PARITY_MAXREPOS`, `MOEDEX_LSP_DEBUG`, `MOEDEX_MCP_URL`, `MOEDEX_TOKEN`.

**Secrets location:**
- `MOEDEX_AUTH_TOKEN` and `GITLAB_TOKEN` are the only secrets. Linux: `/etc/moedex/moedex-serve.env` (`0640 root:moedex`). macOS: `~/.moedex-index/auth-token` (`0600`). Neither is committed; there is no `.env` in the repo.

## Webhooks & Callbacks

**Incoming:**
- No webhooks. The daemon exposes only polled HTTP surfaces:
  - `-http` mode (`cmd/moedex-serve/main.go:712-725`): `GET /healthz`, `GET /metrics`, `GET /stats`, `GET /search` — the last two auth-gated, `/search` behind a concurrency limiter that returns 503 when full.
  - `-mcp-http` mode (`cmd/moedex-serve/main.go:501-512`): `GET /healthz`, `GET /metrics`, and `/mcp` — the MCP Streamable HTTP transport from the official SDK, wired through `internal/mcp/http.go` with a 1 MiB body cap plus the daemon's auth, deadline, panic-recovery, and concurrency middleware.
  - `-mcp` mode: MCP over stdio, one process per agent session.
- MCP tools served: `search_context` (the primary, `internal/mcp/mcp.go:261`), `list_symbols`, `neighbors`, and — only in the `-tags lsp` build — `find_definition`, `find_references`, `find_implementations` (`cmd/moedex-serve/nav_lsp.go:41-55`). Schemas and validation live in `internal/mcp/contracts.go`.

**Outgoing:**
- `POST {MOEDEX_EMBED_URL}/embeddings` — the only outbound HTTP the engine ever makes, and only on the dense `-mcp` path.
- All other outbound traffic is subprocess-initiated: `glab api` (GitLab REST v4 via the CLI) and `git` clone/fetch, both confined to `internal/corpus` and `internal/ingest`.
- No callbacks, no push notifications, no message queue.

---

*Integration audit: 2026-08-24*
