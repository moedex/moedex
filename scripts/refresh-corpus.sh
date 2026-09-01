#!/usr/bin/env bash
# refresh-corpus.sh — refresh the moedex serving corpus end-to-end WITHOUT stalling
# the warm daemon.
#
# Pipeline (fail-stop):
#   1. sync the managed lock + submodules           — moedex corpus sync
#   2. refresh the current shard layout atomically   — refresh OR CAS export
#   3. build graph/ranking sidecars during refresh
#   4. build/refresh the dense embedding sidecar     — moedex serve --build-embeddings
#   5. hot-swap the live daemon                      — SIGHUP → warm reload (no downtime)
#
# Why step 3 exists: token/symbol sidecars rebuild in seconds, but embedding is the
# expensive arm. If the daemon rebuilt the dense store INLINE on reload it would go
# offline for the whole embed. Building it here, BEFORE the reload, means step 4 just
# LOADS a fresh fingerprint-matching sidecar — a fast, zero-downtime hot-swap.
#
# Step 3 is INCREMENTAL: it reuses the vector of every unchanged chunk (keyed by a
# content hash) and embeds only chunks whose text actually changed, so a refresh that
# touched a few repos re-embeds in minutes instead of re-embedding the whole corpus.
#
# Idempotent: with an unchanged corpus, step 2 is a no-op and step 3 finds the store
# already current (the fingerprint matches), so the whole run is cheap. Only changed
# content is re-embedded.
#
# Env (all optional; defaults target this machine):
#   MOEDEX_SHARD_DIR      served shard dir         (default ~/.moedex-state/shards)
#   MOEDEX_CAS_DIR        content-addressable store(default ~/.moedex-state/cas)
#   ONNXRUNTIME_LIB_PATH  onnx runtime dylib       (default /opt/homebrew/lib/libonnxruntime.dylib)
#   MOEDEX_ONNX_INTRA_OP_THREADS ONNX operator threads (default runtime-selected)
#   MOEDEX_ONNX_INTER_OP_THREADS ONNX graph threads    (default runtime-selected)
#   MOEDEX_EMBED          dense embedder           (default onnx)
#   MOEDEX_SERVE_BIN      legacy serve override    (default: unified moe)
#   MOEDEX_INDEX_BIN      legacy index override    (default: unified moe)
#   MOEDEX_CORPUS_BIN     legacy corpus override   (default: unified moe)
#   MOEDEX_CORPUS         managed corpus root      (default ~/.moedex)
#   MOEDEX_LAUNCHD_LABEL  daemon label             (default com.moedex.serve)
#   REFRESH_SYNC=0        skip managed sync; ON by default (needs VPN/glab/Git)
#   REFRESH_SKIP_SHARDS=1 skip step 2 (embeddings-only refresh)

set -euo pipefail
log() { printf '[refresh %s] %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '[refresh] ERROR: %s\n' "$*" >&2; exit 1; }

# Self-sufficient PATH: a launchd `zsh -lc` is a NON-interactive login shell and does
# NOT source ~/.zshrc, where the bin dirs and Homebrew are added — so moedex-corpus,
# glab, and git would otherwise be missing under the timer. Prepend them explicitly,
# with ~/.local/bin (the `make install` canonical dir) FIRST so a stale ~/go/bin copy
# can never win (the binary skew that caused the index-loss incident).
export PATH="$HOME/.local/bin:$HOME/go/bin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

SHARD_DIR="${MOEDEX_SHARD_DIR:-$HOME/.moedex-state/shards}"
CAS_DIR="${MOEDEX_CAS_DIR:-$HOME/.moedex-state/cas}"
CORPUS_ROOT="${MOEDEX_CORPUS:-$HOME/.moedex}"
ONNX_LIB="${ONNXRUNTIME_LIB_PATH:-}"
if [ -z "$ONNX_LIB" ]; then
  for candidate in \
    /opt/homebrew/lib/libonnxruntime.dylib \
    /usr/local/lib/libonnxruntime.dylib \
    /home/linuxbrew/.linuxbrew/lib/libonnxruntime.so \
    /usr/local/lib/libonnxruntime.so \
    /usr/lib/libonnxruntime.so; do
    if [ -e "$candidate" ]; then ONNX_LIB="$candidate"; break; fi
  done
fi
EMBED="${MOEDEX_EMBED:-onnx}"
LABEL="${MOEDEX_LAUNCHD_LABEL:-com.moedex.serve}"
MOE_BIN="${MOEDEX_BIN:-$HOME/.local/bin/moedex}"

if [ -n "${MOEDEX_CORPUS_BIN:-}" ]; then
  CORPUS_SYNC=("$MOEDEX_CORPUS_BIN" sync)
else
  CORPUS_SYNC=("$MOE_BIN" corpus sync)
fi
if [ -n "${MOEDEX_INDEX_BIN:-}" ]; then
  INDEX_DOCTOR=("$MOEDEX_INDEX_BIN" doctor)
  INDEX_REFRESH=("$MOEDEX_INDEX_BIN" refresh)
  INDEX_CAS_REFRESH=("$MOEDEX_INDEX_BIN" cas-refresh)
  INDEX_CAS_EXPORT=("$MOEDEX_INDEX_BIN" cas-export)
else
  INDEX_DOCTOR=("$MOE_BIN" doctor)
  INDEX_REFRESH=("$MOE_BIN" index refresh)
  INDEX_CAS_REFRESH=("$MOE_BIN" index cas refresh)
  INDEX_CAS_EXPORT=("$MOE_BIN" index cas export)
fi
if [ -n "${MOEDEX_SERVE_BIN:-}" ]; then
  SERVE_EMBED=("$MOEDEX_SERVE_BIN")
else
  SERVE_EMBED=("$MOE_BIN" serve)
fi

[ -x "${CORPUS_SYNC[0]}" ] || die "corpus command not found at ${CORPUS_SYNC[0]}"
[ -x "${INDEX_DOCTOR[0]}" ] || die "index command not found at ${INDEX_DOCTOR[0]}"
[ -x "${SERVE_EMBED[0]}" ] || die "dense serve command not found at ${SERVE_EMBED[0]}"
[ -f "$CORPUS_ROOT/.moedex.json" ] || die "managed corpus marker not found at $CORPUS_ROOT; refuse filesystem-discovery fallback"
if [ "$EMBED" = "onnx" ]; then
  [ -n "$ONNX_LIB" ] && [ -e "$ONNX_LIB" ] || die "ONNX Runtime not found; set ONNXRUNTIME_LIB_PATH or install it in a standard Homebrew/system location"
  export ONNXRUNTIME_LIB_PATH="$ONNX_LIB"
fi

corpus_args=(-corpus "$CORPUS_ROOT")

# 1. managed corpus sync. Any partial/unsafe sync stops the pipeline before CAS,
# served shards, sidecars, or the warm daemon can advance.
if [ "${REFRESH_SYNC:-1}" = "1" ]; then
  log "sync: ${CORPUS_SYNC[*]} --corpus $CORPUS_ROOT"
  if ! "${CORPUS_SYNC[@]}" "${corpus_args[@]}" -no-banner; then
    die "managed sync failed; CAS and served snapshot were not advanced"
  fi
else
  log "sync: skipped by REFRESH_SYNC=0; indexing the currently locked snapshot"
fi

# 1b. PREFLIGHT: abort BEFORE the destructive shard swap if the install is unsafe.
#     doctor exits non-zero only on CRITICAL problems (binary skew/shadows, an
#     ambiguous shard-dir layout, no shards) — exactly the conditions that turned a
#     refresh into the index-loss incident. Stale embeddings etc. are WARN, not
#     CRIT, so a normal refresh still proceeds and fixes them.
if [ -f "$SHARD_DIR/manifest.json" ]; then
  log "preflight: ${INDEX_DOCTOR[*]} --shard-dir $SHARD_DIR"
  if ! "${INDEX_DOCTOR[@]}" -shard-dir "$SHARD_DIR"; then
    die "doctor found critical problems — aborting before the destructive refresh"
  fi
else
  log "preflight: no served manifest yet (initial sibling export)"
fi

# 2-3. Select the refresh path from the live shard format. A standard inlined
# build must use `refresh`; a deduped served directory uses CAS delta export.
# Never force one layout over another implicitly.
if [ "${REFRESH_SKIP_SHARDS:-0}" = "1" ]; then
  log "shards: skipped (REFRESH_SKIP_SHARDS=1) — re-embedding over the existing shards"
elif [ -f "$SHARD_DIR/manifest.json" ] && [ ! -f "$SHARD_DIR/blobs.dat" ] && [ ! -f "$SHARD_DIR/blobmanifest.json" ]; then
  log "shards: detected inlined build; ${INDEX_REFRESH[*]} --shard-dir $SHARD_DIR --corpus $CORPUS_ROOT"
  "${INDEX_REFRESH[@]}" -shard-dir "$SHARD_DIR" "${corpus_args[@]}"
elif [ -f "$SHARD_DIR/blobmanifest.json" ] && [ ! -f "$SHARD_DIR/blobs.dat" ]; then
  die "legacy CAS-exported inlined shard layout at $SHARD_DIR is not safe for automatic conversion; follow moedex-index doctor guidance or migrate explicitly"
else
  [ -f "$CAS_DIR/blobmanifest.json" ] || die "CAS manifest not found at $CAS_DIR/blobmanifest.json (build the sibling CAS before scheduling refresh)"
  log "cas: ${INDEX_CAS_REFRESH[*]} --cas-dir $CAS_DIR --corpus $CORPUS_ROOT"
  "${INDEX_CAS_REFRESH[@]}" -cas-dir "$CAS_DIR" "${corpus_args[@]}"
  log "shards: ${INDEX_CAS_EXPORT[*]} --deduped --cas-dir $CAS_DIR --shard-dir $SHARD_DIR"
  "${INDEX_CAS_EXPORT[@]}" -deduped -cas-dir "$CAS_DIR" -shard-dir "$SHARD_DIR"
fi

# 3. dense embedding sidecar — built OUT OF BAND so the reload never re-embeds inline.
log "embeddings: ${SERVE_EMBED[*]} --build-embeddings --embed $EMBED"
ONNXRUNTIME_LIB_PATH="$ONNX_LIB" "${SERVE_EMBED[@]}" -build-embeddings -shard-dir "$SHARD_DIR" -embed "$EMBED" -onnx-runtime "$ONNX_LIB"

# 4. hot-swap the live daemon (warm SIGHUP reload; in-flight requests are not dropped).
if launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
  log "reload: SIGHUP $LABEL (warm hot-swap)"
  launchctl kill -HUP "gui/$(id -u)/$LABEL" || log "reload: SIGHUP failed (daemon down?)"
else
  log "reload: $LABEL not loaded — start it to pick up the refreshed corpus"
fi

# Prune stale refresh backups. Resolve the glob first so an empty backup set is a
# successful no-op under set -o pipefail. Keep the most recent rollback copy.
parent="$(dirname "$SHARD_DIR")"; base="$(basename "$SHARD_DIR")"
shopt -s nullglob
backups=("$parent/$base".bak-* "$parent/$base".dedup-bak-*)
if [ "${#backups[@]}" -gt 1 ]; then
  IFS=$'\n' backups=($(ls -dt "${backups[@]}"))
  unset IFS
  for old in "${backups[@]:1}"; do
    log "prune: removing stale backup $old"
    rm -rf "$old"
  done
fi
log "done."
