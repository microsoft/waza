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

for shard in 0 1 2 3; do
  profile="$scratch/reports/coverage-native-$shard.txt"
  bash "$scripts/go_test_shard.sh" "$shard" "$profile"
  for flag in -race -timeout=30m -covermode=atomic "-coverprofile=$profile"; do
    grep -Fx -- "$flag" "$CALLS" > /dev/null
  done
  pattern="$(awk 'previous == "-run" { print; exit } { previous = $0 }' "$CALLS")"
  test -n "$pattern"
  grep -E -- "$pattern" "$profile.all" > "$scratch/matched"
  diff -u "$profile.selected" "$scratch/matched"
  printf 'mode: atomic\nexample.go:1.1,2.1 1 1\n' > "$profile"
done
printf 'mode: atomic\nexample.go:1.1,2.1 1 1\n' > "$scratch/reports/coverage.txt"
bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"

expect_status 7 env LIST_STATUS=7 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
expect_status 9 env TEST_STATUS=9 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" 4 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" -1 "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" '0;exit 0' "$scratch/profile"
expect_status 2 bash "$scripts/go_test_shard.sh" 0
expect_status 2 bash "$scripts/go_test_shard_verify.sh" "$scratch/missing"

cp "$INVENTORY" "$scratch/saved-inventory"
: > "$INVENTORY"
expect_status 1 bash "$scripts/go_test_shard.sh" 0 "$scratch/profile"
printf 'TestOnly\n' > "$INVENTORY"
expect_status 1 bash "$scripts/go_test_shard.sh" 3 "$scratch/profile"
cp "$scratch/saved-inventory" "$INVENTORY"

profile="$scratch/reports/coverage-native-3.txt"
mv "$profile" "$scratch/saved-profile"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
printf 'mode: set\n' > "$profile"
expect_status 1 bash "$scripts/go_test_shard_verify.sh" "$scratch/reports"
mv "$scratch/saved-profile" "$profile"
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
echo "Native shard discovery, selection, failure propagation, and required artifact verification: PASS"
