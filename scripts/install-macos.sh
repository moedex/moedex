#!/usr/bin/env bash
# install-macos.sh — one-command moedex setup on macOS. IDEMPOTENT: safe to re-run;
# it only changes what's out of date.
#
# What it does:
#   1. check prerequisites (go, make, onnxruntime, glab/git)
#   2. build + install all binaries to ONE canonical dir (make install-dense),
#      removing stale ~/go/bin shadows (the skew that caused the index-loss incident)
#   3. ensure the index dir + a 0600 auth token
#   4. add the moedex env block to ~/.zshrc (PATH, MOEDEX_CORPUS, token) if absent
#   5. render + install the launchd agents (warm daemon + daily refresh) from the
#      deploy/ templates, (re)bootstrapping only when they actually changed
#   6. run `moedex-index doctor` to verify
#
# It initializes a NEW/empty managed corpus when glab + VPN + Git transport are
# ready. It never adopts a populated unmarked corpus and never changes live index
# paths; build and cutover remain explicit rollout steps.
#
# Usage:
#   scripts/install-macos.sh            # do it
#   scripts/install-macos.sh --dry-run  # show what it WOULD do, change nothing
#
# Env overrides: BINDIR (default ~/.local/bin), MOEDEX_CORPUS (default ~/.moedex),
# MOEDEX_INDEX_DIR (default ~/.moedex-index), MOEDEX_CAS_DIR (default
# $MOEDEX_INDEX_DIR/cas), MOEDEX_SHARD_DIR (default $MOEDEX_INDEX_DIR/shards),
# ONNXRUNTIME_LIB_PATH.

set -euo pipefail

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BINDIR="${BINDIR:-$HOME/.local/bin}"
INDEX_DIR="${MOEDEX_INDEX_DIR:-$HOME/.moedex-index}"
CORPUS="${MOEDEX_CORPUS:-$HOME/.moedex}"
CAS_DIR="${MOEDEX_CAS_DIR:-$INDEX_DIR/cas}"
SHARD_DIR="${MOEDEX_SHARD_DIR:-$INDEX_DIR/shards}"
LAUNCH_AGENTS="$HOME/Library/LaunchAgents"
TOKEN_FILE="$INDEX_DIR/auth-token"
CORPUS_BIN="${MOEDEX_CORPUS_BIN:-$BINDIR/moedex-corpus}"

log()  { printf '[setup] %s\n' "$*"; }
warn() { printf '[setup] WARN: %s\n' "$*" >&2; }
die()  { printf '[setup] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

[ "$DRY_RUN" = 1 ] && log "DRY RUN — no changes will be made"
[ "$(uname -s)" = "Darwin" ] || die "this bootstrap is macOS-only (Linux: see deploy/*.service)"

# --- onnx runtime detection (Apple Silicon vs Intel brew prefix) ---
ONNX_LIB=""
for p in "${ONNXRUNTIME_LIB_PATH:-}" /opt/homebrew/lib/libonnxruntime.dylib /usr/local/lib/libonnxruntime.dylib; do
  if [ -n "$p" ] && [ -e "$p" ]; then ONNX_LIB="$p"; break; fi
done

# --- 1. prerequisites ---
log "checking prerequisites"
have go   || die "go not found — install Go 1.26+ first"
have make || die "make not found (install Xcode command line tools)"
have git  || warn "git not found — corpus sync/refresh need it"
have glab || warn "glab not found — corpus sync needs it (brew install glab)"
if [ -n "$ONNX_LIB" ]; then
  log "onnx runtime: $ONNX_LIB"
else
  warn "libonnxruntime not found — the dense arm will be OFF until: brew install onnxruntime"
fi

# --- ownership gate: never adopt a populated legacy/user-owned root ---
CORPUS_NEEDS_INIT=0
if [ -f "$CORPUS/.moedex/corpus.json" ]; then
  log "managed corpus marker present at $CORPUS"
elif [ -e "$CORPUS" ]; then
  shopt -s nullglob dotglob
  corpus_entries=("$CORPUS"/*)
  shopt -u dotglob
  if [ "${#corpus_entries[@]}" -gt 0 ]; then
    die "refusing to adopt populated unmarked corpus $CORPUS. Keep it as rollback; initialize a sibling with: MOEDEX_CORPUS=$CORPUS-managed $BINDIR/moedex-corpus init -corpus $CORPUS-managed"
  fi
  CORPUS_NEEDS_INIT=1
else
  CORPUS_NEEDS_INIT=1
fi

# --- 2. build + install binaries (canonical dir, shadows removed) ---
log "installing binaries to $BINDIR (make install-dense)"
if [ "$DRY_RUN" = 1 ]; then
  log "[dry-run] would run: make -C $REPO install-dense BINDIR=$BINDIR"
else
  make -C "$REPO" install-dense BINDIR="$BINDIR"
fi
case ":$PATH:" in
  *":$BINDIR:"*) : ;;
  *) warn "$BINDIR is not on PATH for THIS shell — the ~/.zshrc block fixes new shells" ;;
esac

# --- 2b. initialize only a new/empty managed corpus ---
if [ "$CORPUS_NEEDS_INIT" = 1 ]; then
  if [ "$DRY_RUN" = 1 ]; then
    log "[dry-run] would initialize managed corpus at $CORPUS (requires active TC VPN, glab auth, and Git transport)"
  else
    [ -x "$CORPUS_BIN" ] || die "moedex-corpus not found at $CORPUS_BIN after install"
    have glab || die "glab is required to initialize the managed corpus"
    have git || die "git is required to initialize the managed corpus"
    log "initializing new managed corpus at $CORPUS"
    "$CORPUS_BIN" init -corpus "$CORPUS" -no-banner
  fi
fi

# --- 3. index dir + auth token ---
if [ "$DRY_RUN" = 1 ]; then
  [ -d "$INDEX_DIR" ] || log "[dry-run] would mkdir $INDEX_DIR"
else
  mkdir -p "$INDEX_DIR"
fi
if [ -f "$TOKEN_FILE" ]; then
  log "auth token present ($TOKEN_FILE)"
elif [ "$DRY_RUN" = 1 ]; then
  log "[dry-run] would generate a 0600 auth token at $TOKEN_FILE"
else
  ( umask 077; openssl rand -hex 32 > "$TOKEN_FILE" )
  chmod 600 "$TOKEN_FILE"
  log "generated auth token at $TOKEN_FILE"
fi

# --- 4. ~/.zshrc env block (PATH, corpus, token) ---
ZRC="$HOME/.zshrc"
if grep -q "MOEDEX_CORPUS" "$ZRC" 2>/dev/null; then
  log "~/.zshrc already configures moedex (MOEDEX_CORPUS found) — leaving it"
elif [ "$DRY_RUN" = 1 ]; then
  log "[dry-run] would append the moedex env block to ~/.zshrc"
else
  {
    echo ""
    echo "# >>> moedex >>>"
    echo 'export PATH="$HOME/.local/bin:$PATH"'
    echo "export MOEDEX_CORPUS=\"$CORPUS\""
    echo 'export MOEDEX_TOKEN="$(cat "$HOME/.moedex-index/auth-token" 2>/dev/null)"'
    echo 'export MOEDEX_AUTH_TOKEN="$MOEDEX_TOKEN"'
    echo "# <<< moedex <<<"
  } >> "$ZRC"
  log "appended moedex env block to ~/.zshrc (open a new shell to load it)"
fi

# --- 5. launchd agents (rendered from deploy/ templates) ---
install_agent() {
  label="$1"; file="$2"
  src="$REPO/deploy/$file"; dst="$LAUNCH_AGENTS/$file"
  [ -f "$src" ] || die "missing template $src"
  rendered="$(sed -e "s|@HOME@|$HOME|g" -e "s|@REPO@|$REPO|g" \
    -e "s|@CORPUS@|$CORPUS|g" -e "s|@CAS_DIR@|$CAS_DIR|g" \
    -e "s|@SHARD_DIR@|$SHARD_DIR|g" -e "s|@ONNX_LIB@|$ONNX_LIB|g" "$src")"
  domain="gui/$(id -u)"
  changed=1
  if [ -f "$dst" ] && [ "$(cat "$dst" 2>/dev/null)" = "$rendered" ]; then changed=0; fi
  if [ "$DRY_RUN" = 1 ]; then
    log "[dry-run] would install $file -> $dst (changed=$changed) and (re)bootstrap $label"
    return 0
  fi
  mkdir -p "$LAUNCH_AGENTS"
  printf '%s\n' "$rendered" > "$dst"
  if [ "$changed" = 1 ] || ! launchctl print "$domain/$label" >/dev/null 2>&1; then
    launchctl bootout "$domain/$label" 2>/dev/null || true
    # launchd can return EIO briefly while an old KeepAlive job is still
    # finishing its asynchronous unload. Retry only this exact rendered plist;
    # fail closed if the job still cannot be registered after the bounded wait.
    bootstrap_ok=0
    if launchctl bootstrap "$domain" "$dst"; then
      bootstrap_ok=1
    else
      warn "$label bootstrap failed while the prior job may still be unloading; retrying for up to 10 seconds"
      for bootstrap_try in {1..10}; do
        sleep 1
        if launchctl bootstrap "$domain" "$dst" 2>/dev/null; then
          bootstrap_ok=1
          break
        fi
      done
    fi
    [ "$bootstrap_ok" = 1 ] || die "$label bootstrap failed after 10 retries; plist retained at $dst"
    log "$label (re)bootstrapped"
  else
    log "$label already current — left running"
  fi
}
log "installing launchd agents"
install_agent com.moedex.serve   com.moedex.serve.plist
install_agent com.moedex.refresh com.moedex.refresh.plist

# --- 6. data-presence hints (index is built/cut over separately) ---
[ -f "$CORPUS/.moedex/corpus.json" ] || [ "$DRY_RUN" = 1 ] || warn "managed corpus marker missing at $CORPUS"
if ! ls "$SHARD_DIR"/*.idx >/dev/null 2>&1; then
  warn "no shard index at $SHARD_DIR — build it once:"
  warn "  moedex-index build -corpus $CORPUS -shard-dir $SHARD_DIR"
  warn "  ONNXRUNTIME_LIB_PATH=$ONNX_LIB moedex-serve -build-embeddings -shard-dir $SHARD_DIR -embed onnx -onnx-runtime $ONNX_LIB"
fi

# --- verify ---
echo
if [ -x "$BINDIR/moedex-index" ]; then
  "$BINDIR/moedex-index" doctor -shard-dir "$SHARD_DIR" || true
elif have moedex-index; then
  moedex-index doctor -shard-dir "$SHARD_DIR" || true
fi
log "done."
