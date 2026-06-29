#!/usr/bin/env bash
# install-lsp-servers.sh — install the language servers the moedex navigation arm
# (ADR 0017, the -tags lsp daemon) drives out of process. IDEMPOTENT: safe to
# re-run; it installs what's missing and leaves the rest alone.
#
# Navigation is OPTIONAL and degrades gracefully: any server you skip just makes
# that language's find_definition/find_references/find_implementations return
# empty. `moedex-index doctor` reports which servers are present (the source of
# truth for the set is the engine's own registry, internal/navigate/registry.go).
#
# What it installs (by the corpus's real weight — C# dominates, then TS, SCSS, …):
#   gopls .......... Go            (go install)
#   csharp-ls ...... C# (~60%)     (.NET SDK via brew + dotnet global tool)
#   typescript-language-server + pyright + vscode-langservers-extracted (css/html/json)
#                    TS/JS, Python, SCSS/CSS, HTML   (npm -g)
#   sql-language-server   SQL      (npm -g; pins a transitive dep that 1.7.1 broke)
#   rust-analyzer .. Rust         (brew; not in the corpus today but kept on request)
#   clangd ......... C/C++        (ships with Xcode Command Line Tools)
#   cflsp .......... CFML (~2.2k)  (OPT-IN: --with-cfml; BUILDS EXTERNAL SOURCE)
#
# Usage:
#   scripts/install-lsp-servers.sh              # core servers (no CFML build)
#   scripts/install-lsp-servers.sh --with-cfml  # also build the CFML server from source
#   scripts/install-lsp-servers.sh --dry-run    # print what it WOULD do, change nothing
#
# After running, ensure these are on your shell PATH (the launchd daemon already
# sets them in deploy/com.moedex.serve.plist; this is for the moedex-nav CLI and
# `moedex-index doctor`):  $HOME/go/bin  $(npm prefix -g)/bin  $HOME/.dotnet/tools
set -euo pipefail

WITH_CFML=0
DRY=0
for a in "$@"; do
  case "$a" in
    --with-cfml) WITH_CFML=1 ;;
    --dry-run)   DRY=1 ;;
    -h|--help)   sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown flag: $a (see --help)" >&2; exit 2 ;;
  esac
done

log()  { printf '[lsp] %s\n' "$*"; }
warn() { printf '[lsp] WARN: %s\n' "$*" >&2; }
have() { command -v "$1" >/dev/null 2>&1; }
run()  { if [ "$DRY" = 1 ]; then printf '[lsp] DRY: %s\n' "$*"; else log "+ $*"; eval "$*"; fi; }

CFML_DIR="${MOEDEX_TOOLS_DIR:-$HOME/.moedex-tools}/cfc"

# --- prerequisites -----------------------------------------------------------
have npm  || warn "npm not found — the TS/Python/CSS/HTML/SQL servers need it (brew install node)"
have brew || warn "brew not found — rust-analyzer and the .NET SDK install via it"
have go   || warn "go not found — gopls installs via 'go install'"

# --- Go: gopls ---------------------------------------------------------------
if have gopls; then log "gopls present: $(command -v gopls)"
elif have go;  then run "go install golang.org/x/tools/gopls@latest"
fi

# --- npm servers: TypeScript, Python, SCSS/CSS, HTML -------------------------
# vscode-langservers-extracted provides vscode-{css,html,json}-language-server.
if have npm; then
  run "npm install -g typescript typescript-language-server pyright vscode-langservers-extracted"
fi

# --- SQL: sql-language-server + dependency pin -------------------------------
# sql-language-server 1.7.1 imports a deep subpath that vscode-languageserver-
# protocol >= 3.18 no longer exports, so a fresh install crashes on startup.
# Pin that transitive dep to the last 3.17 inside the global package. (SQL has no
# LSP navigation regardless — this just keeps the server from erroring.)
if have npm; then
  run "npm install -g sql-language-server"
  GLOBAL_ROOT="$(npm root -g 2>/dev/null || echo)"
  PROTO_DIR="$GLOBAL_ROOT/sql-language-server/node_modules/vscode-languageserver-protocol"
  if [ -d "$PROTO_DIR" ]; then
    run "( cd '$GLOBAL_ROOT/sql-language-server' && npm install vscode-languageserver-protocol@3.17.5 )"
  fi
fi

# --- C#: .NET SDK + csharp-ls ------------------------------------------------
# csharp-ls is a dotnet global tool. The daemon resolves DOTNET_ROOT itself
# (LangSpec.ResolveEnv) so Homebrew's off-path .NET is found at launch.
if have dotnet; then log "dotnet present: $(dotnet --version 2>/dev/null)"
elif have brew; then run "brew install dotnet"
fi
if have csharp-ls; then log "csharp-ls present: $(command -v csharp-ls)"
elif have dotnet;  then run "dotnet tool install --global csharp-ls"
fi

# --- Rust (kept on request; not in the corpus today) -------------------------
if have rust-analyzer; then log "rust-analyzer present: $(command -v rust-analyzer)"
elif have brew;        then run "brew install rust-analyzer"
fi

# --- C/C++: clangd -----------------------------------------------------------
if have clangd; then log "clangd present: $(command -v clangd)"
else warn "clangd not found — install the Xcode Command Line Tools (xcode-select --install)"
fi

# --- CFML: build cflsp from source (OPT-IN) ----------------------------------
# There is no packaged standalone CFML server. The only viable one is built from
# softwareCobbler/cfc (the compiler behind the DavidRogers.cflsp VSCode plugin);
# it implements go-to-DEFINITION only. This BUILDS AND RUNS EXTERNAL SOURCE, so
# it is opt-in (--with-cfml) — your explicit trust decision.
if [ "$WITH_CFML" = 1 ]; then
  if have cflsp; then
    log "cflsp present: $(command -v cflsp)"
  else
    log "building CFML server from softwareCobbler/cfc into $CFML_DIR (external source)"
    run "mkdir -p '$(dirname "$CFML_DIR")'"
    if [ ! -d "$CFML_DIR" ]; then
      run "git clone --depth 1 https://github.com/softwareCobbler/cfc.git '$CFML_DIR'"
    fi
    run "( cd '$CFML_DIR' && npm run install-all && npm run build-cflsp-prod )"
    if [ "$DRY" != 1 ]; then
      SRV="$(grep -rl onDefinition "$CFML_DIR/cflsp-vscode/out"/*.js 2>/dev/null | head -1 || true)"
      [ -n "$SRV" ] || { warn "could not locate the built CFML server.js under $CFML_DIR/cflsp-vscode/out — skipping wrapper"; SRV=""; }
      if [ -n "$SRV" ]; then
        # Install a wrapper on the npm global bin (already on PATH for the others).
        BINDIR="$(npm prefix -g 2>/dev/null)/bin"; [ -d "$BINDIR" ] || BINDIR="/usr/local/bin"
        printf '#!/bin/sh\nexec node "%s" "$@"\n' "$SRV" > "$BINDIR/cflsp"
        chmod +x "$BINDIR/cflsp"
        log "installed cflsp wrapper -> $BINDIR/cflsp (server: $SRV)"
      fi
    fi
  fi
else
  log "skipping CFML (pass --with-cfml to build it from source; it runs external code)"
fi

# --- verify ------------------------------------------------------------------
log "done. Verify with: moedex-index doctor   (see the 'lsp navigation servers' section)"
log "PATH reminder (CLI/doctor): \$HOME/go/bin  \$(npm prefix -g)/bin  \$HOME/.dotnet/tools"
