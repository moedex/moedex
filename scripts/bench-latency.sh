#!/usr/bin/env bash
# bench-latency.sh — end-to-end latency harness for the warm moedex MCP daemon.
#
# Measures real `search_context` round-trip latency over HTTP (the exact path a
# coding agent hits), reporting p50/p90/p95/p99/max per query class. This is the
# black-box, user-perceived number. For per-stage attribution (where the time
# goes inside a query) use the Go benchmarks: `make bench-real`.
#
# Usage:
#   scripts/bench-latency.sh [N]      # N = samples per query (default 20)
# Env:
#   MOEDEX_MCP_URL   (default http://127.0.0.1:8081/mcp)
#   MOEDEX_TOKEN     (default: read ~/.moedex-state/auth-token)
#   BUDGET           token_budget   (default 4000)
#   TOPK             top_k          (default 12)

set -euo pipefail

N="${1:-20}"
URL="${MOEDEX_MCP_URL:-http://127.0.0.1:8081/mcp}"
TOKEN="${MOEDEX_TOKEN:-$(cat "$HOME/.moedex-state/auth-token" 2>/dev/null || true)}"
BUDGET="${BUDGET:-4000}"
TOPK="${TOPK:-12}"

if [[ -z "$TOKEN" ]]; then
  echo "no token: set MOEDEX_TOKEN or create ~/.moedex-state/auth-token" >&2
  exit 1
fi

# Query classes chosen to exercise different cost profiles:
#   selective identifier, common token, multi-word/natural, regex-ish, rare.
QUERIES=(
  "RankConfig"                       # selective identifier
  "func"                             # very common token (large candidate set)
  "intersect posting list"          # multi-word lexical
  "token budget context assembly"   # natural-language-ish (dense arm)
  "embedding ONNX runtime"          # multi-word, vocab-ish
  "SetEnclosingBytes"               # rare symbol
)

req() {
  local q="$1"
  local body
  body=$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_context","arguments":{"query":"%s","token_budget":%s,"top_k":%s}}}' "$q" "$BUDGET" "$TOPK")
  # trailing newline is required: read returns non-zero on an unterminated line,
  # which would trip `set -e`.
  curl -s -o /dev/null -w "%{time_total} %{http_code} %{size_download}\n" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -X POST -d "$body" "$URL"
}

pct() { # pct <p> <sorted-file>  -> value at percentile p (nearest-rank)
  local p="$1" f="$2" n idx
  n=$(wc -l < "$f")
  idx=$(awk -v p="$p" -v n="$n" 'BEGIN{i=int((p/100)*n+0.999999); if(i<1)i=1; if(i>n)i=n; print i}')
  sed -n "${idx}p" "$f"
}

printf "moedex e2e latency  |  url=%s  N=%d  budget=%s  top_k=%s\n" "$URL" "$N" "$BUDGET" "$TOPK"
printf "%-34s %8s %8s %8s %8s %8s %8s\n" "query" "p50" "p90" "p95" "p99" "max" "bytes"
printf '%.0s-' {1..96}; echo

allf=$(mktemp)
for q in "${QUERIES[@]}"; do
  tmp=$(mktemp); bytes=0; fails=0
  for ((i=0;i<N;i++)); do
    read -r t code sz < <(req "$q")
    [[ "$code" == "200" ]] || { fails=$((fails+1)); continue; }
    echo "$t" >> "$tmp"; bytes="$sz"
  done
  sort -n "$tmp" -o "$tmp"
  cat "$tmp" >> "$allf"
  p50=$(pct 50 "$tmp"); p90=$(pct 90 "$tmp"); p95=$(pct 95 "$tmp"); p99=$(pct 99 "$tmp"); mx=$(tail -1 "$tmp")
  suffix=""; [[ "$fails" -gt 0 ]] && suffix="  (!$fails fails)"
  printf "%-34s %7ss %7ss %7ss %7ss %7ss %8s%s\n" "$q" "$p50" "$p90" "$p95" "$p99" "$mx" "$bytes" "$suffix"
  rm -f "$tmp"
done

printf '%.0s-' {1..96}; echo
sort -n "$allf" -o "$allf"
printf "%-34s %7ss %7ss %7ss %7ss %7ss\n" "ALL" "$(pct 50 "$allf")" "$(pct 90 "$allf")" "$(pct 95 "$allf")" "$(pct 99 "$allf")" "$(tail -1 "$allf")"
rm -f "$allf"
