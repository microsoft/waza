#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ] || [[ ! "$1" =~ ^[0-3]$ ]] || [ -z "$2" ]; then
  echo "Usage: bash go_test_shard.sh <shard 0-3> <coverage profile>" >&2
  exit 2
fi

shard="$1"
profile="$2"
scripts="$(cd "$(dirname "$0")" && pwd)"
package=github.com/microsoft/waza/internal/controlledcomparison
inventory="$(go test -race -covermode=atomic -list . "$package")"
names="$(printf '%s\n' "$inventory" |
  awk '/^(Test|Fuzz|Example)[^[:space:]]*$/ { print }' |
  LC_ALL=C sort)"
if [ -z "$names" ]; then
  echo "No runnable native tests discovered; refusing an empty shard." >&2
  exit 1
fi

printf '%s\n' "$names" > "$profile.all"
python3 "$scripts/go_test_shard_plan.py" "$scripts/go_test_shard_durations.tsv" "$shard" \
  < "$profile.all" > "$profile.selected"
pattern=""
while IFS= read -r name; do
  pattern="${pattern:+$pattern|}$name"
done < "$profile.selected"
if [ -z "$pattern" ]; then
  echo "Native shard $shard has no tests; refusing to report success." >&2
  exit 1
fi

echo "Native shard $shard/4 selects $(wc -l < "$profile.selected") of $(wc -l < "$profile.all") discovered tests, examples, and fuzz targets."
go test -v -race -timeout=30m -coverprofile="$profile" -covermode=atomic \
  -run "^($pattern)$" "$package"
