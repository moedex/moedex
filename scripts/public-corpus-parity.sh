#!/bin/sh
# Explicit periodic gate over a caller-provisioned, pinned Roslyn checkout.
set -eu
export GIT_NO_REPLACE_OBJECTS=1
fail() { echo "public-corpus-parity: $*" >&2; exit 2; }
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || fail 'usage: public-corpus-parity.sh CHECKOUT NEW_RUN_DIR [BINARY]'
pin=36d26c5466e4d25940657ccb8d5b9557ccaf7be1
checkout=$(cd "$1" && pwd -P) || fail 'checkout unavailable'
source_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
command -v python3 >/dev/null || fail 'python3 is required for raw source and CSV evidence checks'
check_source() {
    [ "$(git -C "$checkout" rev-parse --show-toplevel)" = "$checkout" ] || fail 'checkout must be the exact repository root'
    [ "$(git -C "$checkout" rev-parse HEAD)" = "$pin" ] || fail 'wrong Roslyn commit'
    state=$(git -C "$checkout" status --porcelain --untracked-files=all --ignored) || fail 'cannot inspect checkout state'
    [ -z "$state" ] || fail 'checkout must be clean, including ignored/untracked files'
    [ "$(git -C "$checkout" ls-files | wc -l | tr -d ' ')" = 35115 ] || fail 'tracked scope changed; review pin and ingestion policy deliberately'
    repos=$(find "$checkout" -name .git -print -prune) || fail 'cannot inspect repository scope'
    [ "$repos" = "$checkout/.git" ] || fail 'checkout must contain exactly one repository'
    python3 "$source_root/scripts/public-corpus-source-check.py" "$checkout" || fail 'raw source verification failed'
}
source_fingerprint=$(check_source) || fail 'source preflight failed'
[ ! -e "$2" ] && [ ! -L "$2" ] || fail 'run directory already exists'
parent=$(cd "$(dirname "$2")" && pwd -P) || fail 'run parent must exist'
leaf=$(basename "$2")
case "$leaf" in ''|.|..) fail 'invalid run directory';; esac
run=$parent/$leaf
case "$run/" in "$checkout/"*) fail 'run directory must be outside checkout';; esac
case "$run/" in "$source_root/"*) fail 'run directory must be outside Moedex source';; esac
if [ "$#" -eq 3 ]; then
    [ -f "$3" ] && [ -x "$3" ] || fail 'supplied binary must be executable'
fi
command -v rg >/dev/null || fail 'ripgrep is required'
if command -v sha256sum >/dev/null; then
    hash_binary() { sha256sum "$1"; }
elif command -v shasum >/dev/null; then
    hash_binary() { shasum -a 256 "$1"; }
else
    fail 'sha256sum or shasum is required'
fi
umask 077
mkdir "$run" || fail 'cannot create new run directory'
if [ "$#" -eq 3 ]; then
    cp "$3" "$run/moedex"
else
    (cd "$source_root" && go build -o "$run/moedex" ./cmd/moedex) >"$run/build.stdout" 2>"$run/build.stderr"
fi
{
    printf 'corpus_commit=%s\ncorpus_root=%s\n' "$pin" "$checkout"
    printf 'corpus_tree=%s\n' "$(git -C "$checkout" rev-parse 'HEAD^{tree}')"
    printf '%s\n' "$source_fingerprint"
    git --version
    rg --version
    go version
    go version -m "$run/moedex" || printf 'Go build metadata unavailable for supplied executable\n'
    hash_binary "$run/moedex"
} >"$run/provenance.txt" 2>&1
set -- "$run/moedex" parity -corpus "$checkout" -work "$run/work" -report "$run/PARITY-REPORT.md" -latency-csv "$run/latency.csv" -seed 20260930 -floor 1000 -scan-parallel 4 -rg-parallel 2 -rg-threads 1 -no-zoekt -keep
# Informational argv; execution below uses the original arguments.
printf '%s\n' "$@" >"$run/argv.txt"
printf '%s\000' "$@" >"$run/argv.nul"
set +e
"$@" >"$run/parity.stdout" 2>"$run/parity.stderr"
status=$?
set -e
printf '%s\n' "$status" >"$run/tool-exit-status.txt"
source_status=0
(check_source) >"$run/source-recheck.txt" 2>&1 || source_status=$?
# Preserve tool failure even when its partial mirror has fewer files.
if [ "$status" -eq 0 ]; then
    [ "$source_status" -eq 0 ] || status=2
    count=$(find "$run/work/mirror" -type f -print | wc -l | tr -d ' ')
    printf '%s\n' "$count" >"$run/mirror-count.txt"
    if [ "$count" != 34791 ]; then
        echo 'eligible mirror scope changed; review pin and ingestion policy deliberately' >&2
        status=2
    fi
    if [ ! -s "$run/PARITY-REPORT.md" ]; then
        echo 'successful tool did not produce a report' >&2
        status=2
    fi
    if ! python3 "$source_root/scripts/public-corpus-source-check.py" --latency "$run/latency.csv" >"$run/latency-check.txt" 2>&1; then
        echo 'latency evidence missing or invalid; see latency-check.txt' >&2
        status=2
    fi
fi
printf '%s\n' "$status" >"$run/exit-status.txt"
printf 'tool_exit=%s\ngate_exit=%s\nsource_recheck_exit=%s\nparity_report=%s\n' "$(cat "$run/tool-exit-status.txt")" "$status" "$source_status" "$run/PARITY-REPORT.md" >"$run/run-report.txt"
printf 'public-corpus-parity: exit %s; evidence %s\n' "$status" "$run"
exit "$status"
