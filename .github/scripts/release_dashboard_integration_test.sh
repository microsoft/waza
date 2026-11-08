#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ] || [ ! -x "$1" ] || [ ! -d web/dist/assets ]; then
  echo "Usage: bash release_dashboard_integration_test.sh <release waza binary>" >&2
  exit 2
fi

binary="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-release-dashboard.XXXXXX")"
source_dist="$scratch/source-dist"
server_pid=""

cleanup() {
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill "$server_pid"
    wait "$server_pid" 2>/dev/null || true
  fi
  if [ -d "$source_dist" ] && [ ! -e web/dist ]; then
    mv "$source_dist" web/dist
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT

mv web/dist "$source_dist"
mkdir -p "$scratch/results"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
"$binary" serve --no-browser --port "$port" --results-dir "$scratch/results" >"$scratch/server.log" 2>&1 &
server_pid=$!
base_url="http://127.0.0.1:$port"

ready=false
for _ in $(seq 1 100); do
  if curl --fail --silent --show-error "$base_url/api/health" > /dev/null 2>&1; then
    ready=true
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    cat "$scratch/server.log" >&2
    echo "Packaged release dashboard server exited before becoming ready." >&2
    exit 1
  fi
  sleep 0.1
done
if [ "$ready" != true ]; then
  cat "$scratch/server.log" >&2
  echo "Packaged release dashboard server did not become ready." >&2
  exit 1
fi

fetch_and_compare() {
  local route="$1"
  local expected="$2"
  local expected_type="$3"
  local body="$scratch/body"
  local headers="$scratch/headers"
  curl --fail --silent --show-error --dump-header "$headers" --output "$body" "$base_url$route"
  cmp "$expected" "$body"
  tr -d '\r' < "$headers" | grep -Eiq "^Content-Type: ${expected_type}"
}

fetch_and_compare "/" "$source_dist/index.html" "text/html"
fetch_and_compare "/favicon.svg" "$source_dist/favicon.svg" "image/svg\\+xml"

asset_count=0
asset_inventory="$scratch/assets.inventory"
if ! find "$source_dist/assets" -type f -print0 > "$asset_inventory"; then
  echo "Packaged release dashboard asset discovery failed." >&2
  exit 1
fi
while IFS= read -r -d '' asset; do
  relative="${asset#"$source_dist"}"
  case "$relative" in
    *.js) expected_type='(text|application)/javascript' ;;
    *.css) expected_type='text/css' ;;
    *) expected_type='[^[:space:]]+' ;;
  esac
  fetch_and_compare "$relative" "$asset" "$expected_type"
  asset_count=$((asset_count + 1))
done < "$asset_inventory"

if [ "$asset_count" -eq 0 ]; then
  echo "Packaged release dashboard contains no served assets." >&2
  exit 1
fi

echo "Packaged release binary served the embedded production dashboard ($asset_count assets)."
