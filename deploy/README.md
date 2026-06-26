# Deploying moedex

> **macOS quick start.** On a Mac, the whole install is one idempotent command:
> ```sh
> ./scripts/install-macos.sh            # build+install binaries, token, launchd agents
> ./scripts/install-macos.sh --dry-run  # preview, change nothing
> ```
> It builds and installs every binary to `~/.local/bin` (removing stale `~/go/bin`
> shadows), writes a 0600 auth token, adds the env block to `~/.zshrc`, and renders
> + bootstraps the `com.moedex.serve` (warm daemon) and `com.moedex.refresh` (daily
> 03:30) launchd agents from the `deploy/*.plist` templates. It does **not** clone
> the corpus or build the index (heavy, need VPN/glab) — it prints those commands.
> Verify anytime with `moedex-index doctor`. The rest of this file is the manual /
> Linux (Docker + systemd) path.

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
| `moedex-index` | Offline shard tool: `build`, `check`, `refresh`, and the CAS family (`cas-build`/`cas-refresh`/`cas-export`) over a corpus. |
| `moedex-corpus` | Corpus setup + freshness: `doctor`, `clone`, `sync` against `gitlab.tcdevops.com` (only). Shells out to `glab`/`git`/`moedex-index`; the engine is never linked into it. |

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

> **Note.** `make build` runs `go build ./...` (no `-o`); there is no make target
> that emits a single plain-daemon binary, so build it with `go build -o ...` as
> above (the Dockerfile does the same).

---

## Standing up the corpus from zero (`moedex-corpus`)

`moedex-corpus` owns corpus *acquisition* and *freshness*: it clones the curated
TurnCommerce repos from `gitlab.tcdevops.com` (and only that host) using your own
glab auth + access levels, then keeps the mirror fresh and re-indexed. It shells
out to `glab`, `git`, and `moedex-index` — the retrieval engine is never linked
into it, so the zero-dependency posture of the daemon is preserved.

Prereqs: the [`glab`](https://gitlab.com/gitlab-org/cli) CLI and `git`. Check the
whole setup first — `doctor` never changes anything:

```sh
moedex-corpus doctor
#   ✓ glab CLI installed   ✓ authenticated to gitlab.tcdevops.com   ✓ git installed
#   repos: N project(s) in scope
# If not authenticated, it prints the exact command to run:
#   glab auth login --hostname gitlab.tcdevops.com
```

**First run** — clone everything and build the served shards in one step:

```sh
moedex-corpus clone \
  -corpus  /srv/moedex/corpus \
  -reindex -cas-dir /srv/moedex/cas -shard-dir /srv/moedex/shards \
  -reload "systemctl reload moedex-serve"
```

This shallow-clones (`--depth 1`) every curated project into
`<corpus>/<group>/<repo>` (many at once; idempotent — re-running skips what's
present), then `cas-build` → `cas-export -deduped` produces a servable shard dir
(the deduped format: content-less shards + one shared `blobs.dat`), then reloads
the daemon. Drop `-reindex` (and the CAS/reload flags) to clone only.

**Scope.** By default the built-in curated allowlist (the TurnCommerce top-level
groups) is used. Override with `-groups FILE`; regenerate a list from an existing
mirror with `moedex-corpus groups --from-disk > corpus-groups.txt`.

**Steady state** — pull fresh + delta re-index + reload:

```sh
moedex-corpus sync \
  -corpus  /srv/moedex/corpus \
  -reindex -cas-dir /srv/moedex/cas -shard-dir /srv/moedex/shards \
  -reload "systemctl reload moedex-serve"
```

`sync` reconciles against the server: clones newly-created repos, fast-forwards
existing ones (fetch + hard reset — shallow-safe, force-push-proof), and
*reports* repos that disappeared on the server. Add `-prune` to also remove those
local repos (it deletes directories — review the "missing" report first). Then
`cas-refresh` adds only net-new blobs and `cas-export -deduped` rewrites only the
changed shards. Automate it with `moedex-sync.timer` (hourly) — see [systemd](#systemd).

> Why shallow: the engine only indexes the working tree at HEAD, so `--depth 1`
> single-branch clones are sufficient and keep the mirror small; LFS blobs are
> skipped (binary — the indexer ignores them anyway).

---

## Building the corpus (shards) — lower-level (`moedex-index`)

`moedex-corpus` (above) drives these for you. Use `moedex-index` directly when the
corpus is **already on disk** (no GitLab/glab needed):

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

## Hardware sizing

Measured on the ~5.2 GB / 484-repo corpus (953 MB indexed); see ADR
[`0001`](../docs/adr/0001-single-node-scope-pure-go-default.md) and
[`0005`](../docs/adr/0005-mmap-compact-postings.md) for the full numbers and the
8 GB projection.

| Phase | Footprint (5.2 GB corpus) | Notes |
| --- | --- | --- |
| **Serve** `-http` (lexical+symbol+path) | ~3 GB RSS (~1 GB heap + reclaimable mmap) | Working set is the **mmap'd postings** (file-backed, reclaimable), not the heap. Comfortable on **8 GB RAM**; ~p50 120 ms / p95 560 ms warm. |
| **Serve** `-mcp` + dense (float32) | ~4 GB heap | Dense store is ~3.5× indexed content. Want **≥16 GB RAM**; `int8` quantization (future) would cut it ~4×. |
| **Build / refresh** (`moedex-index`) | ~10 GB peak | The RAM-binding step (the sidecar build loads all content at once; not shard-bounded). Build on a **≥24–32 GB host**, then ship the shard dir to a modest serve host. |
| **Disk** (servable shard dir) | 2.6 GB (no dense) / 5.4 GB (+dense) | Scales ~linearly to ~4 / ~8 GB at an 8 GB corpus. |

**Rule of thumb:** serving is cheap (postings are mmap'd); building is the
expensive step — separate the build host from the serve host if RAM is tight.

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
# ONNXRUNTIME_VERSION defaults to a known-good release in Dockerfile.dense
# (the binding needs ORT >= 1.27.0); override only for a different runtime:
docker build -f Dockerfile.dense --build-arg ONNXRUNTIME_VERSION=<version> \
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

Files (in `deploy/`): `moedex-serve.service`, `moedex-serve.env.example`, and
**two freshness options — enable ONE:**

- **`moedex-sync.service` + `moedex-sync.timer`** — the complete loop: pull from
  GitLab + per-blob-delta reindex + reload (`moedex-corpus sync -reindex`). Needs
  network + the service identity authenticated to `gitlab.tcdevops.com` + write
  to the corpus tree. **Recommended** for a GitLab-connected host.
- **`moedex-refresh.service` + `moedex-refresh.timer`** — reindex only, from a
  corpus someone else keeps updated on disk (`moedex-index refresh`). No glab/network.

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

### Freshness with `moedex-sync` (the complete loop)

To pull from GitLab *and* re-index *and* reload on a schedule, use the sync units
instead of the refresh units:

```sh
sudo install -m 0755 moedex-corpus /usr/local/bin/moedex-corpus
sudo install -m 0644 deploy/moedex-sync.service /etc/systemd/system/
sudo install -m 0644 deploy/moedex-sync.timer   /etc/systemd/system/
# Set MOEDEX_CORPUS + MOEDEX_CAS_DIR in /etc/moedex/moedex-serve.env, and make
# the service identity able to reach GitLab (see below).
sudo systemctl daemon-reload
sudo systemctl enable --now moedex-sync.timer    # NOT also moedex-refresh.timer
sudo systemctl start moedex-sync                 # run once now
journalctl -u moedex-sync.service -f
```

**Credentials are the load-bearing difference.** `moedex-sync` runs `git`/`glab`
over the network, so the user it runs as must reach `gitlab.tcdevops.com`
non-interactively:

- **glab** — run once *as that user*: `glab auth login --hostname gitlab.tcdevops.com`
  (or set `GITLAB_TOKEN` in the env file — glab reads it).
- **git/SSH** — an SSH key with clone access, and the GitLab host key already in
  that user's `~/.ssh/known_hosts` (the unit keeps `HOME` read-only, so first-
  contact `accept-new` can't write it; pre-seed it, or point
  `GIT_SSH_COMMAND`'s `UserKnownHostsFile` at a writable path).

On a single-owner box it is simplest to set `User=`/`Group=` in
`moedex-sync.service` to **your own account**, reusing your existing glab + ssh —
then nothing extra has to be provisioned.

> **Linux-validate:** like the rest of `deploy/`, these units were authored on
> macOS. Run `systemd-analyze verify deploy/moedex-sync.{service,timer}` and a
> real `systemctl start moedex-sync` on the deploy host; confirm the sandbox
> (`ProtectHome=read-only`, `ReadWritePaths=/srv/moedex`) still lets glab+ssh
> authenticate.

### Why the refresh unit needs a wider write scope

`moedex-index refresh` builds a fresh shard dir and **atomically renames** it
into place. A rename writes the **parent** directory entry, so the refresh unit
declares `ReadWritePaths=/srv/moedex/shards /srv/moedex` (shard dir **and** its
parent). The serve unit only ever reads shards, so it uses
`ReadOnlyPaths=/srv/moedex/shards`.

### Refresh cost at scale

`moedex-index refresh` is **shard-level**: it re-ingests every repo that shares a
shard with a changed repo, so its cost depends on how the changes cluster. With no
repo→shard locality, a **broad** update (many repos advancing at once) touches
every shard and re-ingests ~all repos — about the same cost as a full rebuild, and
it fragments the shard set (one shard per re-ingested repo). It pays off for
**small, frequent** deltas (a few repos between hourly runs); for a large batch, a
full `build` into a fresh dir + `systemctl reload` is equivalent and packs better.
Refresh always serves correct, query-identical content (validated under concurrent
load). See ADR [`0011`](../docs/adr/0011-shard-level-freshness.md).

### Auth token as a secret

`MOEDEX_AUTH_TOKEN` lives in `/etc/moedex/moedex-serve.env` (mode `0640`,
`root:moedex`), loaded via `EnvironmentFile=`. When set, `/search` and `/stats`
require `Authorization: Bearer <token>`; `/healthz` and `/metrics` stay open.
**This is active in the daemon today** (`-auth-token` / `MOEDEX_AUTH_TOKEN` and
the unconditional loopback bind are already in `cmd/moedex-serve`). For stronger
secret handling, swap `EnvironmentFile=` for a systemd credential
(`LoadCredential=` / `systemd-creds`) on hosts that support it.

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
- `moedex-sync.service` specifically: that its relaxed sandbox
  (`ProtectHome=read-only`, `ReadWritePaths=/srv/moedex`) still lets the service
  identity authenticate to GitLab via glab + ssh and clone/pull non-interactively.
- The `parity` CI job (self-hosted runner with corpus + ripgrep).
