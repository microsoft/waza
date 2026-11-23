#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ] || [[ ! "$1" =~ ^[0-3]$ ]] || [ -z "$2" ]; then
  echo "Usage: bash go_remaining_test_shard.sh <shard 0-3> <coverage profile>" >&2
  exit 2
fi

shard="$1"
profile="$2"
scripts="$(cd "$(dirname "$0")" && pwd)"
native_package=github.com/microsoft/waza/internal/controlledcomparison
node "$scripts/ci-evidence.cjs" init-go "$profile.evidence.json"

packages="$(go list ./...)"
native_count=0
: > "$profile.packages.all"
while IFS= read -r package; do
  [ -n "$package" ] || continue
  if [ "$package" = "$native_package" ]; then
    native_count=$((native_count + 1))
  else
    printf '%s\n' "$package" >> "$profile.packages.all"
  fi
done <<< "$packages"
if [ "$native_count" -ne 1 ] || [ ! -s "$profile.packages.all" ]; then
  echo "Package discovery did not contain exactly one native package and a nonempty remainder." >&2
  exit 1
fi
LC_ALL=C sort "$profile.packages.all" -o "$profile.packages.all"

mapfile -t all_packages < "$profile.packages.all"
set +e
go test -p=4 -race -covermode=atomic -json -list . "${all_packages[@]}" |
  node "$scripts/ci-evidence.cjs" discover "$profile.all"
discovery_status=("${PIPESTATUS[@]}")
set -e
finalization_status=0
node "$scripts/ci-evidence.cjs" finish-discovery "$profile.evidence.json" \
  "${discovery_status[0]}" "${discovery_status[1]}" || finalization_status=$?
if [ "${discovery_status[0]}" -ne 0 ]; then exit "${discovery_status[0]}"; fi
if [ "${discovery_status[1]}" -ne 0 ]; then exit "${discovery_status[1]}"; fi
if [ "$finalization_status" -ne 0 ]; then exit "$finalization_status"; fi

python3 -B "$scripts/go_remaining_shard_plan.py" \
  "$scripts/go_remaining_package_durations.tsv" "$shard" \
  < "$profile.packages.all" > "$profile.packages.selected"
python3 -B "$scripts/go_remaining_shard_plan.py" \
  "$scripts/go_remaining_package_durations.tsv" "$shard" --tests "$profile.all" \
  < "$profile.packages.all" > "$profile.selected"
if [ ! -s "$profile.packages.selected" ] || [ ! -s "$profile.selected" ]; then
  echo "Remaining shard $shard has no assigned packages or tests." >&2
  exit 1
fi

mapfile -t selected_packages < "$profile.packages.selected"
echo "Remaining shard $shard/4 selects $(wc -l < "$profile.packages.selected") of $(wc -l < "$profile.packages.all") packages and $(wc -l < "$profile.selected") of $(wc -l < "$profile.all") discovered tests."
set +e
# Preserve framed test output when spinner writes do not end in a newline.
go test -p=4 -count=1 -json -v=test2json -race -timeout=30m \
  -coverprofile="$profile" -covermode=atomic "${selected_packages[@]}" |
  CI_EVIDENCE_INVENTORY_SCOPE=qualified node "$scripts/ci-evidence.cjs" go \
    "$profile.evidence.json" "$profile.all" "$profile.selected"
status=("${PIPESTATUS[@]}")
set -e
finalization_status=0
node "$scripts/ci-evidence.cjs" finish-go "$profile.evidence.json" \
  "${status[0]}" "${status[1]}" || finalization_status=$?
if [ "${status[0]}" -ne 0 ]; then exit "${status[0]}"; fi
if [ "${status[1]}" -ne 0 ]; then exit "${status[1]}"; fi
exit "$finalization_status"
