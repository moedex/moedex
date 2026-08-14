# Deploying moedex

> **macOS quick start.** On a Mac, the whole install is one idempotent command:
> ```sh
> ./scripts/install-macos.sh            # build+install binaries, token, launchd agents
> ./scripts/install-macos.sh --dry-run  # preview, change nothing
> ```
> It builds and installs every binary to `~/.local/bin` (removing stale `~/go/bin`
> shadows), writes a 0600 auth token, adds the env block to `~/.zshrc`, and renders
> + bootstraps the `com.moedex.serve` (warm daemon) and `com.moedex.refresh` (daily
> 14:10 local time) launchd agents from the `deploy/*.plist` templates. For a new
> or empty corpus path it initializes the managed submodule corpus after the
> VPN, `glab`, and Git transport checks pass. It refuses a populated unmarked
> path; index build and live cutover remain explicit sibling-rollout steps.
> Verify anytime with `moedex-index doctor`. The rest of this file is the manual /
> Linux (Docker + systemd) path.
>
> For a validated sibling cutover, pass all three paths together; the installer
> renders the serve and refresh agents from the same values:
> ```sh
> MOEDEX_CORPUS="$HOME/.moedex-managed" \
> MOEDEX_CAS_DIR="$HOME/.moedex-index/cas-managed" \
> MOEDEX_SHARD_DIR="$HOME/.moedex-index/shards-managed" \
> ./scripts/install-macos.sh
> ```

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
| `moedex-corpus` | Corpus setup + freshness: managed `init`/`sync`/`doctor`, plus compatibility `clone`, against `gitlab.tcdevops.com` only. |

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

`moedex-corpus` owns corpus *acquisition* and *freshness*: it creates a local Git
superproject whose stable-ID submodules and committed lock describe the exact
curated default-branch snapshot. It uses your own glab auth + access levels and shells
out to `glab`, `git`, and `moedex-index` — the retrieval engine is never linked
into it, so the zero-dependency posture of the daemon is preserved.

Prereqs: active TC VPN, the [`glab`](https://gitlab.com/gitlab-org/cli) CLI, and
`git`. The VPN session times out after 12 hours, so the macOS refresh runs at
14:10 local time when an operator is more likely to be connected. `doctor` never
changes anything and reports the external layers separately:

```sh
moedex-corpus doctor
#   ✓ authenticated to gitlab.tcdevops.com
#   ✓ GitLab host/VPN reachable
#   ✓ Git clone/fetch transport
#   repos: N project(s) in scope
# If not authenticated, it prints the exact command to run:
#   glab auth login --hostname gitlab.tcdevops.com
```

**First run** — initialize and index a new sibling. Never point `init` at a
populated legacy corpus:

```sh
moedex-corpus init -corpus /srv/moedex/corpus-managed
moedex-index cas-build \
  -corpus /srv/moedex/corpus-managed \
  -cas-dir /srv/moedex/cas-managed
moedex-index cas-export -deduped \
  -cas-dir /srv/moedex/cas-managed \
  -shard-dir /srv/moedex/shards-managed
```

`init` requires a nonexistent or empty destination. A populated unmarked root is
user-owned and is refused with a sibling-root instruction. Keep the old corpus,
CAS, and shards as the immediate rollback set. The compatibility `clone` command
still maintains independent shallow clones, but marked roots are always lock-driven.

**Scope.** By default the built-in curated allowlist (the TurnCommerce top-level
groups) is used. Override with `-groups FILE`; regenerate a list from an existing
mirror with `moedex-corpus groups --from-disk > corpus-groups.txt`.

**Steady state** — fail-stop sync, CAS refresh, deduped export, sidecars, reload:

```sh
MOEDEX_CORPUS=/srv/moedex/corpus-managed \
MOEDEX_CAS_DIR=/srv/moedex/cas-managed \
MOEDEX_SHARD_DIR=/srv/moedex/shards-managed \
scripts/refresh-corpus.sh
```

`sync` reconciles by stable project ID and commits the new lock/gitlink snapshot.
Any partial, auth, VPN, Git transport, missing-source, or lock mismatch failure
stops before CAS/index advancement. `cas-refresh` adds net-new blobs and
`cas-export -deduped` atomically swaps a complete served snapshot. Token/symbol
and dense sidecars are prepared before SIGHUP. The currently served directory is
not replaced on a failed earlier stage.

### Sibling validation, hot-swap, soak, and rollback

Do not edit the running service paths during installation or while building the
sibling. Record the old and new snapshot IDs, redacted project/file/blob counts,
and every gate in
[`ROLLOUT.md`](../docs/plans/phases/02-managed-corpus-integration/ROLLOUT.md).

```sh
moedex-corpus doctor -corpus /srv/moedex/corpus-managed
moedex-index doctor -shard-dir /srv/moedex/shards-managed
moedex-index check \
  -corpus /srv/moedex/corpus-managed \
  -shard-dir /srv/moedex/shards-managed
make parity MOEDEX_CORPUS=/srv/moedex/corpus-managed
```

Only after health, freshness, exact-result parity, and rollback rehearsal pass:

1. Change the service environment to the three `*-managed` sibling paths. On
   macOS, rerun `install-macos.sh` with `MOEDEX_CORPUS`, `MOEDEX_CAS_DIR`, and
   `MOEDEX_SHARD_DIR` set together; it renders and reboots only changed agents.
2. Send SIGHUP if the service process itself did not restart (`launchctl kill -HUP
   gui/$(id -u)/com.moedex.serve` on macOS, `systemctl reload moedex-serve` on Linux).
3. Confirm health and the new shard fingerprint, then begin the recorded soak.
4. Retain the old corpus, CAS, and shards throughout the soak. Rollback is a
   configuration-path restore plus another warm reload; no rebuild is required.

Before relying on the 14:10 macOS run, check each prerequisite independently:

```sh
glab auth status --hostname gitlab.tcdevops.com  # credential only
moedex-corpus doctor -corpus /srv/moedex/corpus-managed  # VPN/API + Git transport + local lock
```

An authenticated `glab` session with an expired/disconnected VPN is expected to
fail the reachability check; a reachable API with broken SSH/HTTPS credentials is
expected to fail the Git transport check.

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
  GitLab + fail-stop managed sync + CAS refresh + deduped export + reload. Needs
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

# 4. sibling managed paths owned by the service user
sudo install -d -o moedex -g moedex \
  /srv/moedex /srv/moedex/corpus-managed /srv/moedex/cas-managed /srv/moedex/shards-managed

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
# Set the three managed sibling paths in /etc/moedex/moedex-serve.env, and make
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
