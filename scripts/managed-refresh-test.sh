#!/usr/bin/env bash
# Hermetic orchestration checks for the managed refresh/install safety boundary.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moedex-managed-refresh.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"
HOME_DIR="$TMP/home"
TRACE="$TMP/trace.log"
mkdir -p "$BIN" "$HOME_DIR"

write_stub() {
  name="$1"
  body="$2"
  printf '#!/usr/bin/env bash\nset -euo pipefail\n%s\n' "$body" > "$BIN/$name"
  chmod +x "$BIN/$name"
}

write_stub moedex-corpus 'printf "corpus %s\n" "$*" >> "$TRACE"; [ "${FAIL_STAGE:-}" != sync ]'
write_stub moedex-index 'printf "index %s\n" "$*" >> "$TRACE"; case "${1:-}" in cas-refresh) [ "${FAIL_STAGE:-}" != cas-refresh ];; cas-export) [ "${FAIL_STAGE:-}" != cas-export ];; esac'
write_stub moedex-serve 'printf "serve %s\n" "$*" >> "$TRACE"'
write_stub launchctl 'printf "launchctl %s\n" "$*" >> "$TRACE"'
write_stub uname 'printf "Darwin\n"'
write_stub go ':'
write_stub make ':'
write_stub git ':'
write_stub glab ':'

fail() {
  printf 'managed-refresh-test: FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  file="$1"; needle="$2"
  grep -F -- "$needle" "$file" >/dev/null || fail "$file does not contain: $needle"
}

assert_not_contains() {
  file="$1"; needle="$2"
  if grep -F -- "$needle" "$file" >/dev/null; then
    fail "$file unexpectedly contains: $needle"
  fi
}

assert_plist_value() {
  file="$1"; key="$2"; expected="$3"
  actual="$(/usr/libexec/PlistBuddy -c "Print:$key" "$file" 2>/dev/null)" ||
    fail "cannot read $key from $file"
  [ "$actual" = "$expected" ] ||
    fail "$file $key = $actual, want $expected"
}

new_fixture() {
  case_dir="$1"
  mkdir -p "$case_dir/corpus/.moedex" "$case_dir/cas" "$case_dir/shards"
  printf '{}\n' > "$case_dir/corpus/.moedex/corpus.json"
  printf '{}\n' > "$case_dir/cas/blobmanifest.json"
  printf '{}\n' > "$case_dir/shards/manifest.json"
  printf 'keep-serving\n' > "$case_dir/shards/live-sentinel"
  : > "$TRACE"
}

run_refresh() {
  case_dir="$1"
  env \
    HOME="$HOME_DIR" \
    PATH="$BIN:$PATH" \
    TRACE="$TRACE" \
    FAIL_STAGE="${FAIL_STAGE:-}" \
    MOEDEX_CORPUS="$case_dir/corpus" \
    MOEDEX_CAS_DIR="$case_dir/cas" \
    MOEDEX_SHARD_DIR="$case_dir/shards" \
    MOEDEX_CORPUS_BIN="$BIN/moedex-corpus" \
    MOEDEX_INDEX_BIN="$BIN/moedex-index" \
    MOEDEX_SERVE_BIN="$BIN/moedex-serve" \
    bash "$REPO/scripts/refresh-corpus.sh"
}

# Success proves strict stage order and that zero backup matches are a no-op
# under pipefail.
success="$TMP/success"
new_fixture "$success"
run_refresh "$success" >/dev/null
expected="$TMP/expected.log"
printf '%s\n' \
  "corpus sync -corpus $success/corpus -no-banner" \
  "index doctor -shard-dir $success/shards" \
  "index cas-refresh -cas-dir $success/cas -corpus $success/corpus" \
  "index cas-export -deduped -cas-dir $success/cas -shard-dir $success/shards" \
  "serve -build-embeddings -shard-dir $success/shards -embed onnx -onnx-runtime /opt/homebrew/lib/libonnxruntime.dylib" \
  "launchctl print gui/$(id -u)/com.moedex.serve" \
  "launchctl kill -HUP gui/$(id -u)/com.moedex.serve" > "$expected"
cmp -s "$expected" "$TRACE" || {
  diff -u "$expected" "$TRACE" >&2 || true
  fail "refresh stages ran out of order"
}

# A managed sync failure stops before CAS/index/sidecars/reload and leaves the
# currently served directory untouched.
sync_fail="$TMP/sync-fail"
new_fixture "$sync_fail"
if FAIL_STAGE=sync run_refresh "$sync_fail" >/dev/null 2>&1; then
  fail "sync failure returned success"
fi
assert_contains "$TRACE" "corpus sync"
assert_not_contains "$TRACE" "cas-refresh"
assert_not_contains "$TRACE" "serve "
assert_not_contains "$TRACE" "launchctl kill"
assert_contains "$sync_fail/shards/live-sentinel" "keep-serving"

# A CAS failure likewise stops before export, sidecars, and reload.
cas_fail="$TMP/cas-fail"
new_fixture "$cas_fail"
if FAIL_STAGE=cas-refresh run_refresh "$cas_fail" >/dev/null 2>&1; then
  fail "CAS refresh failure returned success"
fi
assert_contains "$TRACE" "index cas-refresh"
assert_not_contains "$TRACE" "index cas-export"
assert_not_contains "$TRACE" "serve "
assert_not_contains "$TRACE" "launchctl kill"
assert_contains "$cas_fail/shards/live-sentinel" "keep-serving"

# Install refuses implicit adoption and prints the sibling-root procedure before
# build, launchd, or live-path mutation.
install_case="$TMP/install-refusal"
mkdir -p "$install_case/corpus" "$install_case/home"
printf 'user-owned\n' > "$install_case/corpus/README.txt"
install_log="$TMP/install.log"
if env HOME="$install_case/home" PATH="$BIN:$PATH" MOEDEX_CORPUS="$install_case/corpus" \
  BINDIR="$BIN" bash "$REPO/scripts/install-macos.sh" --dry-run > "$install_log" 2>&1; then
  fail "install accepted a populated unmarked corpus"
fi
assert_contains "$install_log" "refusing to adopt populated unmarked corpus"
assert_contains "$install_log" "$install_case/corpus-managed"
assert_contains "$install_case/corpus/README.txt" "user-owned"

# A real hermetic render proves the three sibling paths stay coherent across
# the serving and scheduled-refresh agents. This catches a dangerous split-brain
# configuration where launchd refreshes the managed corpus but serves legacy shards.
render_case="$TMP/install-render"
render_home="$render_case/home"
render_corpus="$render_case/corpus-managed"
render_cas="$render_case/cas-managed"
render_shards="$render_case/shards-managed"
mkdir -p "$render_home" "$render_corpus/.moedex" "$render_cas" "$render_shards"
printf '{}\n' > "$render_corpus/.moedex/corpus.json"
env HOME="$render_home" PATH="$BIN:$PATH" TRACE="$TRACE" \
  BINDIR="$BIN" \
  MOEDEX_INDEX_DIR="$render_case/index" \
  MOEDEX_CORPUS="$render_corpus" \
  MOEDEX_CAS_DIR="$render_cas" \
  MOEDEX_SHARD_DIR="$render_shards" \
  bash "$REPO/scripts/install-macos.sh" >/dev/null 2>&1
serve_plist="$render_home/Library/LaunchAgents/com.moedex.serve.plist"
refresh_plist="$render_home/Library/LaunchAgents/com.moedex.refresh.plist"
serve_args="$(/usr/libexec/PlistBuddy -c 'Print:ProgramArguments:2' "$serve_plist")"
case "$serve_args" in
  *'-shard-dir "'"$render_shards"'"'*) : ;;
  *) fail "serve plist does not use managed shard path" ;;
esac
assert_plist_value "$refresh_plist" 'EnvironmentVariables:MOEDEX_CORPUS' "$render_corpus"
assert_plist_value "$refresh_plist" 'EnvironmentVariables:MOEDEX_CAS_DIR' "$render_cas"
assert_plist_value "$refresh_plist" 'EnvironmentVariables:MOEDEX_SHARD_DIR' "$render_shards"
assert_plist_value "$refresh_plist" 'StartCalendarInterval:Hour' '13'

printf 'managed-refresh-test: PASS\n'
