#!/usr/bin/env bash
set -euo pipefail

scripts="$(cd "$(dirname "$0")" && pwd)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-release-test.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
mkdir "$scratch/runner temp"
RUNNER_TEMP="$scratch/runner temp" bash "$scripts/go_offline_env.sh" > "$scratch/environment"
while IFS='=' read -r name value; do
  export "$name=$value"
done < "$scratch/environment"
test "$ENABLE_COPILOT_TESTS" = false
test "$WAZA_MCP_RUNTIME_TEST" = false
test "$WAZA_EVIDENCE_JUDGE_RUNTIME_TEST" = false
test "$WAZA_RESPONDER_RUNTIME_TEST" = false
test "$NO_COLOR" = 1
[[ "$COPILOT_CLI_PATH" = /* ]]
test -x "$COPILOT_CLI_PATH"
test ! -e "$COPILOT_CLI_PATH.invoked"

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

expect_status 64 "$COPILOT_CLI_PATH" --version
test -f "$COPILOT_CLI_PATH.invoked"
rm "$COPILOT_CLI_PATH.invoked"
expect_status 1 env RUNNER_TEMP="$scratch/missing" bash "$scripts/go_offline_env.sh"

cat > "$scratch/waza" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test "$ENABLE_COPILOT_TESTS" = false
test "$NO_COLOR" = 1
test "$#" -eq 5
test "$1" = run
test "$2" = ./examples/code-explainer/eval.yaml
test "$3" = --verbose
test "$4" = --output
if [ -n "${OUTCOME_SOURCE:-}" ]; then
  cp "$OUTCOME_SOURCE" "$5"
fi
if [ "${TRY_RUNTIME:-false}" = true ]; then
  "$COPILOT_CLI_PATH" --version || true
fi
exit "${WAZA_STATUS:-0}"
EOF
chmod +x "$scratch/waza"
for status in 0 1; do
  expect_status 1 env WAZA_STATUS="$status" bash "$scripts/release_integration_test.sh" "$scratch/waza"
  grep -F 'lacks a complete error-free mock outcome' "$scratch/output"
  test ! -e "$COPILOT_CLI_PATH.invoked"
done
for status in 2 64 124 130 137 143; do
  expect_status "$status" env WAZA_STATUS="$status" bash "$scripts/release_integration_test.sh" "$scratch/waza"
done
for status in 0 1; do
  expect_status 1 env TRY_RUNTIME=true WAZA_STATUS="$status" bash "$scripts/release_integration_test.sh" "$scratch/waza"
  grep -F 'attempted to launch the Copilot runtime' "$scratch/output"
  expect_status 1 bash "$scripts/release_integration_test.sh" "$scratch/waza"
  grep -F 'earlier forbidden Copilot runtime launch' "$scratch/output"
  rm "$COPILOT_CLI_PATH.invoked"
done
expect_status 2 bash "$scripts/release_integration_test.sh"
expect_status 2 env COPILOT_CLI_PATH=relative bash "$scripts/release_integration_test.sh" "$scratch/waza"
expect_status 2 env COPILOT_CLI_PATH="$scratch/missing" bash "$scripts/release_integration_test.sh" "$scratch/waza"
expect_status 127 bash "$scripts/release_integration_test.sh" "$scratch/missing-waza"
echo "Offline runtime proof and release integration failure propagation: PASS"
