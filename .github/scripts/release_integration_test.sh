#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ] || [[ "${COPILOT_CLI_PATH:-}" != /* ]] || [ ! -x "$COPILOT_CLI_PATH" ]; then
  echo "Usage: COPILOT_CLI_PATH=<absolute rejecting guard> bash release_integration_test.sh <waza binary>" >&2
  exit 2
fi

if [ -e "$COPILOT_CLI_PATH.invoked" ]; then
  echo "Release integration found an earlier forbidden Copilot runtime launch." >&2
  exit 1
fi

scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-release-integration.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
status=0
"$1" run ./examples/code-explainer/eval.yaml --verbose --output "$scratch/outcome.json" || status=$?
if [ -e "$COPILOT_CLI_PATH.invoked" ]; then
  echo "Release integration attempted to launch the Copilot runtime." >&2
  exit 1
fi
if [ "$status" -gt 1 ]; then
  echo "Release integration failed with exit code $status." >&2
  exit "$status"
fi
if ! jq -e -s --argjson status "$status" '
  def nonempty_string: type == "string" and length > 0;
  def completed_run($task_grader):
    (.status == "passed" or .status == "failed") and
    (has("error_msg") | not) and
    (.validations | type == "object" and length > 0) and
    (.validations | keys) == (["_output_contains", $task_grader, "has_explanation", "no_errors"] | sort) and
    all(.validations | to_entries[];
      .key == .value.identifier and
      (.value.passed | type == "boolean") and
      (.value.score | type == "number" and . >= 0 and . <= 1)
    ) and
    ((.status == "passed") == all(.validations[]; .passed));
  {
    "explain-js-async-001": "explains_async_pattern",
    "explain-list-comprehension-001": "explains_comprehension",
    "explain-python-recursion-001": "explains_recursion",
    "explain-sql-join-001": "explains_sql_join"
  } as $task_graders |
  length == 1 and
  (.[0] |
    (.schemaVersion | nonempty_string) and
    (.eval_id | nonempty_string) and
    (.timestamp | nonempty_string) and
    .eval_name == "code-explainer-eval" and .skill == "code-explainer" and
    .config.engine_type == "mock" and .config.runs_per_test == 3 and
    (.tasks | type == "array") and
    ([.tasks[].test_id] | sort) == ($task_graders | keys) and
    all(.tasks[];
      $task_graders[.test_id] as $task_grader |
      ((has("cached") | not) or .cached == false) and
      (.status == "passed" or .status == "failed") and
      (.runs | type == "array") and
      ([.runs[].run_number] | sort) == [1, 2, 3] and
      all(.runs[]; completed_run($task_grader)) and
      ((.status == "passed") == all(.runs[]; .status == "passed"))
    ) and
    .summary.total_tests == 4 and .summary.errors == 0 and .summary.skipped == 0 and
    .summary.succeeded == ([.tasks[] | select(.status == "passed")] | length) and
    .summary.failed == ([.tasks[] | select(.status == "failed")] | length) and
    (if $status == 0 then .summary.failed == 0 else .summary.failed > 0 end)
  )
' "$scratch/outcome.json" > /dev/null; then
  echo "Release integration lacks a complete error-free mock outcome matching exit code $status." >&2
  exit 1
fi
echo "Offline mock release integration completed (exit code: $status)."
