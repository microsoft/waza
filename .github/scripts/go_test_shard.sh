#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ] || [[ ! "$1" =~ ^[0-4]$ ]] || [ -z "$2" ]; then
  echo "Usage: bash go_test_shard.sh <shard 0-4> <coverage profile>" >&2
  exit 2
fi

shard="$1"
profile="$2"
package=github.com/microsoft/waza/internal/controlledcomparison
inventory="$(go test -race -covermode=atomic -list . "$package")"
names="$(printf '%s\n' "$inventory" |
  awk '/^(Test|Fuzz|Example)[^[:space:]]*$/ { print }' |
  LC_ALL=C sort)"
if [ -z "$names" ]; then
  echo "No runnable native tests discovered; refusing an empty shard." >&2
  exit 1
fi
for required in TestNativeReaderFrozen15cDecisionParity TestNativeCollectionObservedFailures TestNativeLateFailurePreservesEveryStartedAttempt; do
  if ! grep -Fx -- "$required" <<< "$names" > /dev/null; then
    echo "Missing required native test $required; update the reserved shard policy and its controls before changing discovery." >&2
    exit 1
  fi
done

printf '%s\n' "$names" > "$profile.all"
: > "$profile.selected"
index=0
pattern=""
while IFS= read -r name; do
  case "$name" in
    TestNativeReaderFrozen15cDecisionParity) selected_shard=0 ;;
    TestNativeCollectionObservedFailures) selected_shard=1 ;;
    TestNativeLateFailurePreservesEveryStartedAttempt) selected_shard=2 ;;
    *)
      selected_shard=$((2 + index % 3))
      index=$((index + 1))
      ;;
  esac
  if [ "$selected_shard" -eq "$shard" ]; then
    printf '%s\n' "$name" >> "$profile.selected"
    pattern="${pattern:+$pattern|}$name"
  fi
done <<< "$names"
if [ -z "$pattern" ]; then
  echo "Native shard $shard has no tests; refusing to report success." >&2
  exit 1
fi

echo "Native shard $shard/5 selects $(wc -l < "$profile.selected") of $(wc -l < "$profile.all") discovered tests, examples, and fuzz targets."
go test -v -race -timeout=30m -coverprofile="$profile" -covermode=atomic \
  -run "^($pattern)$" "$package"
