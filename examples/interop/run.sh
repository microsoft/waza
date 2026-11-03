#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
waza="${1:?Pass an absolute path to the source-built Waza binary}"
case "$waza" in
  /*) ;;
  *) echo "Pass an absolute binary path" >&2; exit 2 ;;
esac
command -v jq >/dev/null
scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-interop-example.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT

adapter=agentv-68df6d2206cea61f721d0445b405efcb5e94aff0-contains-v1
"$waza" interop import --adapter "$adapter" --test contains-check --assertion 1 \
  < "$root/internal/interop/testdata/agentv/suite.yaml" > "$scratch/grader.yaml"
"$waza" interop export --adapter "$adapter" < "$scratch/grader.yaml"

cat > "$scratch/eval.yaml" <<'EOF'
schemaVersion: "1.0"
name: assertion-conformance
skill: assertion-conformance
version: "1.0"
config:
  executor: mock
  model: mock-model
  trials_per_task: 1
  timeout_seconds: 30
tasks: [positive.yaml, negative.yaml]
metrics: []
graders:
EOF
awk '!/^#/ { if (!first++) print "  - " $0; else print "    " $0 }' \
  "$scratch/grader.yaml" >> "$scratch/eval.yaml"
cat > "$scratch/positive.yaml" <<'EOF'
id: positive
name: Positive control
inputs:
  prompt: Hello
EOF
cat > "$scratch/negative.yaml" <<'EOF'
id: negative
name: Negative control
inputs:
  prompt: hello
EOF
status=0
(
  cd "$scratch"
  WAZA_NO_UPDATE_CHECK=1 "$waza" run eval.yaml --output results.json
) || status=$?
if [ "$status" -ne 1 ]; then
  echo "Expected completed negative control exit 1, got $status" >&2
  exit 1
fi
jq -e '
  .summary.total_tests == 2 and .summary.succeeded == 1 and
  .summary.failed == 1 and .summary.errors == 0 and
  (.tasks | length) == 2 and
  ([.tasks[] | select(.test_id == "positive") |
    .runs[0].validations["agentv-contains"] | .passed == true and .score == 1] == [true]) and
  ([.tasks[] | select(.test_id == "negative") |
    .runs[0].validations["agentv-contains"] | .passed == false and .score == 0] == [true])
' "$scratch/results.json" >/dev/null
echo "Selected assertion only: positive=1, negative=0; mock conformance, not suite compatibility."
