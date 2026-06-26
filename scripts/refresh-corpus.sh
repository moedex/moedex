#!/usr/bin/env bash
# refresh-corpus.sh — refresh the moedex serving corpus end-to-end WITHOUT stalling
# the warm daemon.
#
# Pipeline:
#   1. (optional) sync the corpus from gitlab      — moedex-corpus sync   (REFRESH_SYNC=1)
#   2. refresh shards + token/symbol sidecars      — moedex-index [cas-]refresh
#   3. build/refresh the dense embedding sidecar    — moedex-serve -build-embeddings   ← the missing piece
#   4. hot-swap the live daemon                      — SIGHUP → warm reload (no downtime)
#
# Why step 3 exists: token/symbol sidecars rebuild in seconds, but the dense store is
# expensive (a full-corpus embed). If the daemon rebuilt it INLINE on reload it would
# go offline for the whole embed. Building it here, BEFORE the reload, means step 4 just
# LOADS a fresh fingerprint-matching sidecar — a fast, zero-downtime hot-swap.
#
# Idempotent: with an unchanged corpus, step 2 is a no-op and step 3 loads the cached
# store (the fingerprint matches), so the whole run is cheap. Only a real content change
# triggers a re-embed.
#
# Env (all optional; defaults target this machine):
#   MOEDEX_SHARD_DIR      shard/CAS dir            (default ~/.moedex-index/shards)
#   ONNXRUNTIME_LIB_PATH  onnx runtime dylib       (default /opt/homebrew/lib/libonnxruntime.dylib)
#   MOEDEX_EMBED          dense embedder           (default onnx)
#   MOEDEX_SERVE_BIN      dense moedex-serve       (default ~/.local/bin/moedex-serve)
#   MOEDEX_INDEX_BIN      moedex-index             (default: ~/go/bin, ~/.local/bin, or PATH)
#   MOEDEX_CORPUS         corpus root for sync     (default ~/.moedex; refresh uses the manifest Root)
#   MOEDEX_LAUNCHD_LABEL  daemon label             (default com.moedex.serve)
#   REFRESH_SYNC=1        run step 1 (corpus pull); OFF by default (needs glab auth)
#   REFRESH_SKIP_SHARDS=1 skip step 2 (embeddings-only refresh)

set -euo pipefail
log() { printf '[refresh %s] %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '[refresh] ERROR: %s\n' "$*" >&2; exit 1; }

SHARD_DIR="${MOEDEX_SHARD_DIR:-$HOME/.moedex-index/shards}"
ONNX_LIB="${ONNXRUNTIME_LIB_PATH:-/opt/homebrew/lib/libonnxruntime.dylib}"
EMBED="${MOEDEX_EMBED:-onnx}"
SERVE_BIN="${MOEDEX_SERVE_BIN:-$HOME/.local/bin/moedex-serve}"
LABEL="${MOEDEX_LAUNCHD_LABEL:-com.moedex.serve}"

INDEX_BIN="${MOEDEX_INDEX_BIN:-}"
if [ -z "$INDEX_BIN" ]; then
  for c in "$HOME/go/bin/moedex-index" "$HOME/.local/bin/moedex-index" "$(command -v moedex-index 2>/dev/null || true)"; do
    if [ -n "$c" ] && [ -x "$c" ]; then INDEX_BIN="$c"; break; fi
  done
fi

[ -x "$SERVE_BIN" ] || die "dense moedex-serve not found at $SERVE_BIN (set MOEDEX_SERVE_BIN; must be the -tags onnx build)"
[ -d "$SHARD_DIR" ] || die "shard dir not found: $SHARD_DIR"

# 1. corpus sync (optional — needs glab auth available to this session)
if [ "${REFRESH_SYNC:-0}" = "1" ]; then
  if command -v moedex-corpus >/dev/null 2>&1; then
    log "sync: moedex-corpus sync"
    # Non-fatal: a failed pull (e.g. glab auth unavailable to a headless timer) just
    # means we refresh over the current on-disk corpus rather than aborting the run.
    MOEDEX_CORPUS="${MOEDEX_CORPUS:-$HOME/.moedex}" moedex-corpus sync || log "sync: FAILED (continuing with on-disk corpus)"
  else
    log "sync: REFRESH_SYNC=1 but moedex-corpus not on PATH — skipping"
  fi
else
  log "sync: skipped (set REFRESH_SYNC=1 to pull the corpus first)"
fi

# 2. shards + token/symbol sidecars (atomic rebuild-and-swap). CAS dir (blobs.dat) uses
#    cas-refresh -cas-dir; a legacy dir uses refresh -shard-dir.
if [ "${REFRESH_SKIP_SHARDS:-0}" = "1" ]; then
  log "shards: skipped (REFRESH_SKIP_SHARDS=1) — re-embedding over the existing shards"
elif [ -z "$INDEX_BIN" ]; then
  log "shards: moedex-index not found — skipping (set MOEDEX_INDEX_BIN); re-embedding over existing shards"
elif [ -f "$SHARD_DIR/blobs.dat" ]; then
  log "shards: $INDEX_BIN cas-refresh -cas-dir $SHARD_DIR"
  "$INDEX_BIN" cas-refresh -cas-dir "$SHARD_DIR" ${MOEDEX_CORPUS:+-corpus "$MOEDEX_CORPUS"}
else
  log "shards: $INDEX_BIN refresh -shard-dir $SHARD_DIR"
  "$INDEX_BIN" refresh -shard-dir "$SHARD_DIR" ${MOEDEX_CORPUS:+-corpus "$MOEDEX_CORPUS"}
fi

# 3. dense embedding sidecar — built OUT OF BAND so the reload never re-embeds inline.
log "embeddings: $SERVE_BIN -build-embeddings -embed $EMBED"
ONNXRUNTIME_LIB_PATH="$ONNX_LIB" "$SERVE_BIN" -build-embeddings -shard-dir "$SHARD_DIR" -embed "$EMBED" -onnx-runtime "$ONNX_LIB"

# 4. hot-swap the live daemon (warm SIGHUP reload; in-flight requests are not dropped).
if launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
  log "reload: SIGHUP $LABEL (warm hot-swap)"
  launchctl kill -HUP "gui/$(id -u)/$LABEL" || log "reload: SIGHUP failed (daemon down?)"
else
  log "reload: $LABEL not loaded — start it to pick up the refreshed corpus"
fi
log "done."
