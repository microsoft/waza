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
for profile in coverage.txt coverage-native-{0,1,2,3,4}.txt; do
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
LC_ALL=C sort -u "$reference" > "$scratch/unique"
diff -u "$reference" "$scratch/unique"
printf '%s\n' TestNativeReaderFrozen15cDecisionParity > "$scratch/reserved-0"
printf '%s\n' TestNativeCollectionObservedFailures > "$scratch/reserved-1"
for shard in 0 1; do
  if ! diff -u "$scratch/reserved-$shard" "$directory/coverage-native-$shard.txt.selected"; then
    echo "Native shard $shard must contain only its exact reserved test; update shard policy and controls together." >&2
    exit 1
  fi
done
if ! grep -Fx TestNativeLateFailurePreservesEveryStartedAttempt "$directory/coverage-native-2.txt.selected" > /dev/null; then
  echo "Native shard 2 must include TestNativeLateFailurePreservesEveryStartedAttempt." >&2
  exit 1
fi
LC_ALL=C sort "$scratch/selected" > "$scratch/sorted"
diff -u "$reference" "$scratch/sorted"
echo "All five native shards passed with identical discovery and exhaustive, nonoverlapping selection."
