# Deploying moedex

Operational guide for running the moedex retrieval daemon (`moedex-serve`) and
the offline shard tool (`moedex-index`). Two deployment shapes are covered:

1. **Docker** — the zero-dependency default image, plus an optional dense (ONNX)
   image.
2. **systemd** — the daemon as a hardened service, with an hourly atomic shard
   refresh on a timer.

> **Honesty note.** These artifacts were authored and verified on macOS. Docker
> image builds and `systemd-analyze`/`systemctl` checks must be validated on a
> **Linux deploy host** — they cannot be exercised on macOS. Items needing
> Linux validation are called out inline.

---

## Components

| Binary         | Role                                                                 |
| -------------- | ------------------------------------------------------------------- |
| `moedex-serve` | Warm retrieval daemon. `-http` (HTTP API), `-mcp` (agent context), or `-q` (one-shot). mmaps shards once; SIGHUP hot-reload; SIGINT/SIGTERM 5s graceful drain. |
| `moedex-index` | Offline shard tool: `build`, `check`, `refresh` over a corpus.       |

HTTP routes on `-http`: `/healthz` (200 `ok`), `/metrics`, `/stats`, `/search`.
`/healthz` and `/metrics` stay open even with auth enabled.

---

## Building the binaries (no Docker)

```sh
go build -o moedex-serve ./cmd/moedex-serve
go build -o moedex-index ./cmd/moedex-index
# dense (in-process ONNX embedder), mirrors `make build-dense`:
make build-dense          # -> ./moedex-serve-dense  (needs ONNXRUNTIME_LIB_PATH at run time)
```

> **Gap (reported by Lane C).** There is no `make` target that emits a single
> binary for the plain daemon — `make build` is `go build ./...` with no `-o`.
> The Dockerfile and the commands above call `go build -o ...` directly. This
> was **not** added to the Makefile (out of fence); flag it if you want a target.

---

## Building the corpus (shards)

```sh
# Build a servable shard dir from a corpus root (every git repo beneath it):
moedex-index build -corpus /srv/moedex/corpus -shard-dir /srv/moedex/shards

# Verify a shard dir against its manifest (and optionally the corpus):
moedex-index check -shard-dir /srv/moedex/shards

# Atomically rebuild + swap in fresh shards (corpus root defaults to the
# manifest's recorded Root):
moedex-index refresh -shard-dir /srv/moedex/shards
```

`MOEDEX_CORPUS` is the env fallback for `build -corpus`.

---

## Docker

### Default image (zero dependencies)

Pure-Go lexical + symbol + path retrieval. CGO-free, no build tags, no ONNX.

```sh
docker build -t moedex-serve .

# Run: publish to host loopback ONLY, behind a trusted proxy; shards READ-ONLY
# (the -http daemon only reads shards); require Bearer auth.
docker run -d --name moedex-serve \
  -p 127.0.0.1:8080:8080 \
  -v /srv/moedex/shards:/shards:ro \
  -e MOEDEX_AUTH_TOKEN="$(openssl rand -hex 32)" \
  moedex-serve
```

- The daemon binds `127.0.0.1` inside the container (it enforces a loopback
  default), and `-p 127.0.0.1:8080:8080` publishes only to the host loopback.
  Put nginx/caddy in front for network exposure.
- `HEALTHCHECK` probes `/healthz` with `wget`.
- **`-ro` vs `-rw`:** the `-http` daemon only reads shards, so mount `:ro`. The
  **`-mcp`** path (and the dense arm) persist an embedding cache next to the
  shards and need `:rw`.

### Dense image (optional, ONNX in-process embedder)

```sh
docker build -f Dockerfile.dense -t moedex-serve-dense .
# override the ONNX Runtime version if needed:
docker build -f Dockerfile.dense --build-arg ONNXRUNTIME_VERSION=1.21.0 \
  -t moedex-serve-dense .

# Dense arm is -mcp only (stdio). Shard dir MUST be writable (embedding cache):
docker run --rm -i \
  -v /srv/moedex/shards:/shards \
  moedex-serve-dense
```

- Built with `-tags onnx`; bundles `libonnxruntime.so*` from the official
  `microsoft/onnxruntime` release and sets `ONNXRUNTIME_LIB_PATH`.
- Runtime base is `debian:12-slim` (glibc) — alpine's musl cannot host
  `libonnxruntime.so`.
- **Linux-validate:** confirm the build succeeds and the binding dlopens the
  runtime. The Dockerfile tries `CGO_ENABLED=0` first (the binding dlopens at
  run time); if a Linux build fails, rebuild with `CGO_ENABLED=1` + a C
  toolchain (see the comment in `Dockerfile.dense`).

---

## systemd

Files (in `deploy/`): `moedex-serve.service`, `moedex-refresh.service`,
`moedex-refresh.timer`, `moedex-serve.env.example`.

### Install

```sh
# 1. dedicated user
sudo useradd -r -s /usr/sbin/nologin moedex   # skip if it exists

# 2. binaries
sudo install -m 0755 moedex-serve /usr/local/bin/moedex-serve
sudo install -m 0755 moedex-index /usr/local/bin/moedex-index

# 3. config + secret (0640 root:moedex)
sudo install -d -m 0750 -o root -g moedex /etc/moedex
sudo install -m 0640 -o root -g moedex \
  deploy/moedex-serve.env.example /etc/moedex/moedex-serve.env
sudoedit /etc/moedex/moedex-serve.env     # set MOEDEX_SHARD_DIR + MOEDEX_AUTH_TOKEN

# 4. shard dir owned by the service user
sudo install -d -o moedex -g moedex /srv/moedex /srv/moedex/shards

# 5. units
sudo install -m 0644 deploy/moedex-serve.service   /etc/systemd/system/
sudo install -m 0644 deploy/moedex-refresh.service /etc/systemd/system/
sudo install -m 0644 deploy/moedex-refresh.timer   /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now moedex-serve
sudo systemctl enable --now moedex-refresh.timer
```

> If `MOEDEX_SHARD_DIR` differs from `/srv/moedex/shards`, also edit the
> `ReadOnlyPaths` (serve unit) and `ReadWritePaths` (refresh unit) lines — those
> paths are hardcoded in the unit files because systemd sandbox directives are
> evaluated before `EnvironmentFile` expansion.

### Operate

```sh
sudo systemctl reload moedex-serve     # SIGHUP -> hot-reload shards, no dropped requests
sudo systemctl restart moedex-serve    # SIGTERM -> 5s drain, then restart
sudo systemctl start moedex-refresh    # rebuild shards now (also runs hourly via the timer)
systemctl list-timers moedex-refresh.timer
journalctl -u moedex-serve -f
```

### Why the refresh unit needs a wider write scope

`moedex-index refresh` builds a fresh shard dir and **atomically renames** it
into place. A rename writes the **parent** directory entry, so the refresh unit
declares `ReadWritePaths=/srv/moedex/shards /srv/moedex` (shard dir **and** its
parent). The serve unit only ever reads shards, so it uses
`ReadOnlyPaths=/srv/moedex/shards`.

### Auth token as a secret

`MOEDEX_AUTH_TOKEN` lives in `/etc/moedex/moedex-serve.env` (mode `0640`,
`root:moedex`), loaded via `EnvironmentFile=`. When set, `/search` and `/stats`
require `Authorization: Bearer <token>`; `/healthz` and `/metrics` stay open.
**This is active in the daemon today** (`-auth-token` / `MOEDEX_AUTH_TOKEN` and
the unconditional loopback bind are already in `cmd/moedex-serve`). For stronger
secret handling, swap `EnvironmentFile=` for a systemd credential
(`LoadCredential=` / `systemd-creds`) on hosts that support it.

> The plan described `MOEDEX_AUTH_TOKEN` as "not yet consumed." That is now
> **stale** — the auth lane has landed and the daemon reads it. No deferral.

### Cron alternative to the timer

If you prefer cron over the systemd timer, drop the timer and add (as a user who
can `systemctl reload`, or wrap in sudo):

```cron
# m h dom mon dow   command  — hourly atomic shard refresh + hot reload
0 * * * *  /usr/local/bin/moedex-index refresh -shard-dir /srv/moedex/shards && systemctl reload moedex-serve
```

---

## CI (`.gitlab-ci.yml`)

- **`gate` stage** — `make health` (build + vet + test) on every push and MR.
  Runs on the shared `golang:1.26` image.
- **`parity` stage** — `make parity` (full-corpus ripgrep parity) ONLY on
  **scheduled pipelines or tags**. Requires a **self-hosted runner** tagged
  `moedex-corpus` that has:
  - the corpus on disk at `$MOEDEX_CORPUS` (defaults to `~/TCGitlab` if unset),
  - **ripgrep (`rg`)** installed,
  - disk headroom for the `.parity-work` scratch dir.
  Publishes `PARITY-REPORT.md` as an artifact.
- **`build-images` (manual)** — builds + pushes the default and dense images to
  `$CI_REGISTRY_IMAGE`. Needs a Docker-capable runner (dind or docker socket).

`GOTOOLCHAIN: local` prevents CI from auto-downloading a Go toolchain.

---

## What still needs a Linux host to validate

- `docker build .` and `docker build -f Dockerfile.dense .` (image builds).
- The dense ONNX build path (CGO need; ONNX Runtime ↔ binding compatibility;
  release-asset URL/layout for the pinned version).
- `systemd-analyze verify deploy/*.service deploy/*.timer` and an actual
  `systemctl enable --now` cycle (sandbox directives, reload→SIGHUP wiring).
- The `parity` CI job (self-hosted runner with corpus + ripgrep).
