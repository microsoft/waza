#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 RESULT_FILE MIN_SUCCESS_RATE RUN_EXIT_CODE" >&2
  exit 2
fi

result_file="$1"
min_success_rate="$2"
run_exit_code="$3"

if ! jq -en --arg value "$min_success_rate" '
  ($value | tonumber?) as $number |
  $number != null and $number >= 0 and $number <= 1
' >/dev/null; then
  echo "::error::Minimum success rate must be a number from 0 through 1" >&2
  exit 1
fi

case "$run_exit_code" in
  ''|*[!0-9]*)
    echo "::error::Waza run exit code is not a non-negative integer: $run_exit_code" >&2
    exit 1
    ;;
esac

if [ ! -f "$result_file" ] || [ ! -s "$result_file" ]; then
  echo "::error::Waza did not produce a non-empty result artifact at $result_file" >&2
  exit 1
fi

contract='
  def integer: type == "number" and isfinite and floor == .;
  .config.runs_per_test as $trials |
  type == "object" and
  (.schemaVersion | type == "string" and length > 0) and
  (.eval_id | type == "string" and length > 0) and
  (.config.runs_per_test | integer and . > 0) and
  (.summary | type == "object") and
  (.tasks | type == "array") and
  ([.tasks[].test_id] | length == (unique | length)) and
  (all(.tasks[];
    type == "object" and
    (.test_id | type == "string" and length > 0) and
    (.status | IN("passed", "failed", "error", "skipped")) and
    (.runs | type == "array") and
    (.runs | length <= $trials) and
    ([.runs[].run_number] == [range(1; (.runs | length) + 1)]) and
    (all(.runs[];
      type == "object" and
      (.run_number | integer and . > 0) and
      (.attempts | integer and . > 0) and
      (.status | IN("passed", "failed", "error", "skipped")) and
      (.duration_ms | integer and . >= 0) and
      (.final_output | type == "string"))) and
    (if .status == "passed" then
      (.runs | length == $trials) and
      (any(.runs[]; .status == "passed")) and
      (all(.runs[]; .status == "passed" or .status == "skipped"))
    else true end))) and
  (.summary.total_tests | integer and . >= 0) and
  (.summary.succeeded | integer and . >= 0) and
  (.summary.failed | integer and . >= 0) and
  (.summary.errors | integer and . >= 0) and
  (.summary.skipped | integer and . >= 0) and
  (.summary.success_rate | type == "number" and isfinite and . >= 0 and . <= 1) and
  (.summary.aggregate_score | type == "number" and isfinite) and
  (.summary.weighted_score | type == "number" and isfinite) and
  (.summary.total_tests == (.tasks | length)) and
  (.summary.total_tests ==
    (.summary.succeeded + .summary.failed + .summary.errors + .summary.skipped)) and
  (.summary.succeeded == ([.tasks[] | select(.status == "passed")] | length)) and
  (.summary.failed == ([.tasks[] | select(.status == "failed")] | length)) and
  (.summary.errors == ([.tasks[] | select(.status == "error")] | length)) and
  (.summary.skipped == ([.tasks[] | select(.status == "skipped")] | length)) and
  (.summary.success_rate ==
    (if .summary.total_tests == 0 then 0
     else .summary.succeeded / .summary.total_tests end))
'

if ! jq -e -s "length == 1 and (.[0] | $contract)" < "$result_file" >/dev/null 2>&1; then
  echo "::error::Waza result artifact is malformed, incomplete, or internally inconsistent: $result_file" >&2
  exit 1
fi

IFS=$'\t' read -r success_rate aggregate_score total_tests succeeded failed errors skipped all_runs_passed < <(
  jq -r '
    [.summary.success_rate, .summary.aggregate_score, .summary.total_tests,
     .summary.succeeded, .summary.failed, .summary.errors, .summary.skipped,
     (all(.tasks[].runs[]; .status == "passed"))]
    | @tsv
  ' < "$result_file"
)

if [ "$run_exit_code" -eq 0 ] && { [ "$failed" -ne 0 ] || [ "$errors" -ne 0 ]; }; then
  echo "::error::Waza exited successfully but the result artifact reports failed or errored tasks" >&2
  exit 1
fi

status=failure
if [ "$run_exit_code" -eq 0 ] &&
  [ "$total_tests" -gt 0 ] &&
  [ "$skipped" -eq 0 ] &&
  [ "$all_runs_passed" = true ] &&
  jq -en --arg rate "$success_rate" --arg minimum "$min_success_rate" \
    '($rate | tonumber) >= ($minimum | tonumber)' >/dev/null; then
  status=success
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "success_rate=$success_rate"
    echo "aggregate_score=$aggregate_score"
    echo "status=$status"
  } >> "$GITHUB_OUTPUT"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "## Waza evaluation"
    echo
    echo "| Result | Value |"
    echo "| --- | ---: |"
    echo "| Status | $status |"
    echo "| Success rate | $success_rate |"
    echo "| Aggregate score | $aggregate_score |"
    echo "| Tasks | $total_tests |"
    echo "| Succeeded | $succeeded |"
    echo "| Failed | $failed |"
    echo "| Errors | $errors |"
    echo "| Skipped | $skipped |"
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$run_exit_code" -ne 0 ]; then
  echo "::error::Waza evaluation exited with code $run_exit_code" >&2
elif [ "$status" != "success" ]; then
  echo "::error::Evaluation has no fully graded task inventory or success rate $success_rate is below required minimum $min_success_rate" >&2
fi
