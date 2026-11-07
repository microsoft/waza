#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ] || [[ "$1" != all && "$1" != remaining ]] || [ -z "$2" ]; then
  echo "Usage: bash ci_go_test_capture.sh <all|remaining> <coverage profile>" >&2
  exit 2
fi

scope="$1"
profile="$2"
scripts="$(cd "$(dirname "$0")" && pwd)"
node "$scripts/ci-evidence.cjs" init-go "$profile.evidence.json"

if [ "$scope" = remaining ]; then
  packages="$(go list ./...)"
  selected=()
  while IFS= read -r package; do
    if [ "$package" != github.com/microsoft/waza/internal/controlledcomparison ]; then
      selected+=("$package")
    fi
  done <<< "$packages"
  if [ "${#selected[@]}" -eq 0 ]; then
    echo "No remaining packages discovered; refusing an empty execution." >&2
    exit 1
  fi
else
  selected=(./...)
fi

set +e
go test -race -covermode=atomic -json -list . "${selected[@]}" |
  node "$scripts/ci-evidence.cjs" discover "$profile.all"
discovery_status=("${PIPESTATUS[@]}")
set -e
node "$scripts/ci-evidence.cjs" finish-discovery "$profile.evidence.json" "${discovery_status[0]}" "${discovery_status[1]}"
if [ "${discovery_status[0]}" -ne 0 ]; then exit "${discovery_status[0]}"; fi
if [ "${discovery_status[1]}" -ne 0 ]; then exit "${discovery_status[1]}"; fi
cp "$profile.all" "$profile.selected"
set +e
go test -count=1 -json -v -race -timeout=30m -coverprofile="$profile" -covermode=atomic \
  "${selected[@]}" |
  node "$scripts/ci-evidence.cjs" go "$profile.evidence.json" "$profile.all" "$profile.selected"
status=("${PIPESTATUS[@]}")
set -e
node "$scripts/ci-evidence.cjs" finish-go "$profile.evidence.json" "${status[0]}" "${status[1]}"
if [ "${status[0]}" -ne 0 ]; then exit "${status[0]}"; fi
exit "${status[1]}"
