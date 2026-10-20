#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ] || [ ! -d "$1" ]; then
  echo "Usage: bash go_test_shard_verify.sh <coverage artifact directory>" >&2
  exit 2
fi
directory="$1"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-shard-verify.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
reference="$directory/coverage-native-0.txt.all"
: > "$scratch/selected"
for profile in coverage.txt coverage-native-{0,1,2,3}.txt; do
  path="$directory/$profile"
  if [ ! -s "$path" ] || [ "$(head -n 1 "$path")" != "mode: atomic" ]; then
    echo "Missing or invalid atomic coverage profile: $path" >&2
    exit 1
  fi
  if [ "$profile" = coverage.txt ]; then continue; fi
  if [ ! -s "$path.all" ] || [ ! -s "$path.selected" ]; then
    echo "Missing or empty native test inventory: $path" >&2
    exit 1
  fi
  cmp "$reference" "$path.all"
  cat "$path.selected" >> "$scratch/selected"
done
LC_ALL=C sort "$scratch/selected" > "$scratch/sorted"
diff -u "$reference" "$scratch/sorted"
echo "All four native shards passed with identical discovery and exhaustive, nonoverlapping selection."
