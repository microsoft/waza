#!/usr/bin/env bash
set -euo pipefail

scripts="$(cd "$(dirname "$0")" && pwd)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-shard-test.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
mkdir "$scratch/bin" "$scratch/reports"
cat > "$scratch/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" -list . "* ]]; then
  if [[ " $* " != *" -race "* ]] || [[ " $* " != *" -covermode=atomic "* ]]; then
    echo "Discovery must use the same race and coverage build." >&2
    exit 88
  fi
  if [ "${LIST_STATUS:-0}" -ne 0 ]; then exit "$LIST_STATUS"; fi
  cat "$INVENTORY"
  exit 0
fi
printf '%s\n' "$@" > "$CALLS"
exit "${TEST_STATUS:-0}"
EOF
chmod +x "$scratch/bin/go"
export PATH="$scratch/bin:$PATH"
export INVENTORY="$scratch/inventory" CALLS="$scratch/calls"
cat > "$INVENTORY" <<'EOF'
TestZulu
FuzzBytes
TestAlpha
TestNativeReaderFrozen15cDecisionParity
TestNativeReaderFrozen15cDecisionParityExtra
TestNativeCollectionObservedFailures
TestNativeCollectionObservedFailuresExtra
TestNativeLateFailurePreservesEveryStartedAttempt
TestNativeLateFailurePreservesEveryStartedAttemptExtra
ExampleComparison
TestNativeCollectionProductionLifecycle
FuzzUnicode
TestNativeReaderSuppliedDecisionProcess
BenchmarkReadSelectedDecisionActualCollectionCapture
ok github.com/microsoft/waza/internal/controlledcomparison 0.001s
EOF
printf 'Test\316\251mega\n' >> "$INVENTORY"

expect_status() {
  local expected="$1" status=0
  shift
  "$@" > "$scratch/output" 2>&1 || status=$?
  if [ "$status" -ne "$expected" ]; then
    cat "$scratch/output" >&2
    echo "Expected status $expected, got $status: $*" >&2
    exit 1
  fi
}

for shard in 0 1 2 3 4; do
  profile="$scratch/reports/coverage-native-$shard.txt"
  bash "$scripts/go_test_shard.sh" "$shard" "$profile"
  for flag in -race -timeout=30m -covermode=atomic "-coverprofile=$profile"; do
    grep -Fx -- "$flag" "$CALLS" > /dev/null
  done
  pattern="$(awk 'previous == "-run" { print; exit } { previous = $0 }' "$CALLS")"
  test -n "$pattern"
  grep -E -- "$pattern" "$profile.all" > "$scratch/matched"
  diff -u "$profile.selected" "$scratch/matched"
  case "$shard" in
    0) printf '%s\n' TestNativeReaderFrozen15cDecisionParity ;;
    1) printf '%s\n' TestNativeCollectionObservedFailures ;;
    2) printf '%s\n' ExampleComparison TestAlpha TestNativeLateFailurePreservesEveryStartedAttempt TestNativeLateFailurePreservesEveryStartedAttemptExtra TestZulu ;;
    3) printf '%s\n' FuzzBytes TestNativeCollectionObservedFailuresExtra TestNativeReaderFrozen15cDecisionParityExtra; printf 'Test\316\251mega\n' ;;
    4) printf '%s\n' FuzzUnicode TestNativeCollectionProductionLifecycle TestNativeReaderSuppliedDecisionProcess ;;
  esac > "$scratch/expected-$shard"
  diff -u "$scratch/expected-$shard" "$profile.selected"
  bash "$scripts/go_test_shard.sh" "$shard" "$scratch/repeated"
  diff -u "$profile.selected" "$scratch/repeated.selected"
  printf 'mode: atomic\nexample.go:1.1,2.1 1 1\n' > "$profile"
done
printf 'mode: atomic\nexample.go:1.1,2.1 1 1\n' > "$scratch/reports/coverage.txt"
bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"

expect_status 7 env LIST_STATUS=7 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
expect_status 9 env TEST_STATUS=9 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" 5 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" -1 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" '0;exit 0' "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" 0
expect_status 2 bash "$scripts/go_test_shard.sh" 00 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" 0 ""
expect_status 2 bash "$scripts/go_test_shard_verify.sh" "$scratch/missing"

cp "$INVENTORY" "$scratch/saved-inventory"
: > "$INVENTORY"
expect_status 1 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
printf '%s\n' TestNativeReaderFrozen15cDecisionParity TestNativeCollectionObservedFailures TestNativeLateFailurePreservesEveryStartedAttempt TestOnly > "$INVENTORY"
expect_status 1 bash "$scripts/go_test_shard.sh" 3 "$scratch/profile"
grep -F 'Native shard 3 has no tests' "$scratch/output" > /dev/null
cp "$scratch/saved-inventory" "$INVENTORY"

for required in TestNativeReaderFrozen15cDecisionParity TestNativeCollectionObservedFailures TestNativeLateFailurePreservesEveryStartedAttempt; do
  grep -Fxv "$required" "$scratch/saved-inventory" > "$INVENTORY"
  expect_status 1 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
  grep -F "Missing required native test $required" "$scratch/output" > /dev/null
done
cp "$scratch/saved-inventory" "$INVENTORY"

# Reordered discovery must not change placement; new targets must still be exhaustive.
LC_ALL=C sort -r "$scratch/saved-inventory" > "$INVENTORY"
for shard in 0 1 2 3 4; do
  bash "$scripts/go_test_shard.sh" "$shard" "$scratch/reordered"
  diff -u "$scratch/expected-$shard" "$scratch/reordered.selected"
done
printf '%s\n' ExampleAdded TestNew FuzzAdded >> "$INVENTORY"
mkdir "$scratch/changed"
for shard in 0 1 2 3 4; do
  profile="$scratch/changed/coverage-native-$shard.txt"
  bash "$scripts/go_test_shard.sh" "$shard" "$profile"
  printf 'mode: atomic\nexample.go:1.1,2.1 1 1\n' > "$profile"
done
cp "$scratch/reports/coverage.txt" "$scratch/changed/coverage.txt"
bash "$scripts/go_test_shard_verify.sh" "$scratch/changed"
cp "$scratch/saved-inventory" "$INVENTORY"

for profile in "$scratch/reports/coverage.txt" "$scratch/reports"/coverage-native-{0,1,2,3,4}.txt; do
  mv "$profile" "$scratch/saved-profile"
  expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
  printf 'mode: set\n' > "$profile"
  expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
  : > "$profile"
  expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
  mv "$scratch/saved-profile" "$profile"
done
profile="$scratch/reports/coverage-native-4.txt"
cp "$profile.selected" "$scratch/saved-selected"
: > "$profile.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/saved-selected" "$profile.selected"
head -n 1 "$profile.selected" >> "$profile.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/saved-selected" "$profile.selected"
sed '$d' "$profile.selected" > "$scratch/truncated"
mv "$scratch/truncated" "$profile.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/saved-selected" "$profile.selected"
printf 'TestUnexpected\n' >> "$profile.all"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/reports/coverage-native-0.txt.all" "$profile.all"

mv "$scratch/reports/coverage-native-0.txt.selected" "$scratch/saved-reserved"
cp "$scratch/reports/coverage-native-1.txt.selected" "$scratch/reports/coverage-native-0.txt.selected"
cp "$scratch/saved-reserved" "$scratch/reports/coverage-native-1.txt.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/reports/coverage-native-0.txt.selected" "$scratch/reports/coverage-native-1.txt.selected"
mv "$scratch/saved-reserved" "$scratch/reports/coverage-native-0.txt.selected"

profile="$scratch/reports/coverage-native-2.txt"
cp "$profile.selected" "$scratch/saved-late"
grep -Fxv TestNativeLateFailurePreservesEveryStartedAttempt "$profile.selected" > "$scratch/without-late"
mv "$scratch/without-late" "$profile.selected"
printf '%s\n' TestNativeLateFailurePreservesEveryStartedAttempt >> "$scratch/reports/coverage-native-3.txt.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
cp "$scratch/saved-late" "$profile.selected"
sed '$d' "$scratch/reports/coverage-native-3.txt.selected" > "$scratch/restored"
mv "$scratch/restored" "$scratch/reports/coverage-native-3.txt.selected"
bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"

for suffix in all selected; do
  profile="$scratch/reports/coverage-native-4.txt.$suffix"
  mv "$profile" "$scratch/saved-inventory-artifact"
  expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
  : > "$profile"
  expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
  mv "$scratch/saved-inventory-artifact" "$profile"
done
for shard in 0 1 2 3 4; do
  profile="$scratch/reports/coverage-native-$shard.txt.all"
  printf '%s\n' TestZulu >> "$profile"
  LC_ALL=C sort "$profile" > "$scratch/duplicate-inventory"
  mv "$scratch/duplicate-inventory" "$profile"
done
printf '%s\n' TestZulu >> "$scratch/reports/coverage-native-2.txt.selected"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
echo "Native shard discovery, selection, failure propagation, and required artifact verification: PASS"
