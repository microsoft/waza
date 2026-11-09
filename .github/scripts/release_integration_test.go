package scripts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const releaseCaptureLauncher = `#!/usr/bin/env bash
set -uo pipefail
if [ "$1" = preflight ]; then exec "$REAL_WAZA" "$@"; fi
bash -c 'printf "%s" "$$" > "$CAPTURE_PID"; exec "$REAL_WAZA" "$@"' bash "$@"
status=$?
printf '%s' "$status" > "$CAPTURE_STATUS"
if [ -f "$5" ]; then cp "$5" "$CAPTURE_OUTCOME" || exit 74; fi
exit "$status"
`

func TestReleaseIntegrationProductionOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release shell gate executes on Ubuntu; SIGTERM controls require Unix")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	canonical, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for dir := canonical; ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == ".copilot" {
			t.Fatal("release controls require TMPDIR outside .copilot ancestry")
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Fatal("release controls require TMPDIR outside checkout ancestry")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	binary := filepath.Join(scratch, "waza")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/waza")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building real release CLI: %v\n%s", err, output)
	}
	gate := filepath.Join(root, ".github", "scripts", "release_integration_test.sh")
	var passing, failing []byte
	for _, mode := range []string{
		"passed", "grader-failure", "grader-exception", "task-error",
		"invalid-regex", "valid-regex-failure", "checkpoint-exception", "checkpoint-passed",
		"hook-after-run", "hook-after-task", "retry-exception", "retry-unused", "missing-program",
	} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			example := filepath.Join(dir, "examples", "code-explainer")
			if err := os.MkdirAll(filepath.Dir(example), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(example, os.DirFS(filepath.Join(root, "examples", "code-explainer"))); err != nil {
				t.Fatal(err)
			}
			task := filepath.Join(example, "tasks", "explain-python-recursion.yaml")
			data, err := os.ReadFile(task)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "grader-failure", "checkpoint-exception", "checkpoint-passed":
				original := string(data)
				data = []byte(strings.Replace(original, `"len(output) > 10"`, `"len(output) < 0"`, 1))
				if original == string(data) {
					t.Fatal("grader-failure control did not change the actual task assertion")
				}
				if strings.HasPrefix(mode, "checkpoint-") {
					assertion := "True"
					if mode == "checkpoint-exception" {
						assertion = "1 / 0"
					}
					data = append(data, []byte(fmt.Sprintf(`
checkpoints:
  - after_turn: 1
    graders:
      - name: intermediate
        type: code
        config:
          assertions: [%q]
`, assertion))...)
				}
			case "grader-exception":
				original := string(data)
				data = []byte(strings.Replace(original, `"len(output) > 10"`, `"1 / 0"`, 1))
				if original == string(data) {
					t.Fatal("exception control did not change the actual assertion")
				}
			case "retry-exception":
				marker := strconv.Quote(filepath.Join(dir, "first-attempt"))
				assertion := fmt.Sprintf(
					`re.__builtins__["__import__"]("pathlib").Path(%s).exists() or (re.__builtins__["__import__"]("pathlib").Path(%s).write_text("first") and 1 / 0)`,
					marker, marker)
				original := string(data)
				data = []byte(strings.Replace(original, `"len(output) > 10"`, strconv.Quote(assertion), 1))
				if original == string(data) {
					t.Fatal("retry control did not change the actual assertion")
				}
			case "task-error":
				data = append(data, []byte("\ninstruction_files:\n  - missing-release-instruction.md\n")...)
			}
			writeReleaseFile(t, task, data)
			if strings.HasPrefix(mode, "hook-") || strings.HasPrefix(mode, "retry-") || mode == "missing-program" {
				evalPath := filepath.Join(example, "eval.yaml")
				source, err := os.ReadFile(evalPath)
				if err != nil {
					t.Fatal(err)
				}
				changed := string(source)
				switch {
				case strings.HasPrefix(mode, "hook-"):
					hook := strings.ReplaceAll(strings.TrimPrefix(mode, "hook-"), "-", "_")
					changed += fmt.Sprintf("\nhooks:\n  %s:\n    - command: \"false\"\n      error_on_fail: true\n", hook)
				case strings.HasPrefix(mode, "retry-"):
					changed = strings.Replace(changed, "  parallel: false", "  parallel: false\n  max_attempts: 2", 1)
				case mode == "missing-program":
					changed = strings.Replace(changed,
						"- type: code\n    name: has_explanation\n    config:\n      assertions:\n        - \"len(output) > 10\"",
						"- type: program\n    name: has_explanation\n    config:\n      command: "+strconv.Quote(filepath.Join(dir, "missing-grader")), 1)
				}
				if changed == string(source) {
					t.Fatal("production admission control did not change the actual eval")
				}
				writeReleaseFile(t, evalPath, []byte(changed))
			}
			if mode == "invalid-regex" || mode == "valid-regex-failure" {
				evalPath := filepath.Join(example, "eval.yaml")
				source, err := os.ReadFile(evalPath)
				if err != nil {
					t.Fatal(err)
				}
				pattern := "["
				if mode == "valid-regex-failure" {
					pattern = "."
				}
				changed := strings.Replace(string(source), "(?i)fatal error|crashed|exception occurred", pattern, 1)
				if changed == string(source) {
					t.Fatal("regex control did not change the actual grader configuration")
				}
				writeReleaseFile(t, evalPath, []byte(changed))
			}
			env, guard := releaseControlEnvironment(t, dir, root)
			traceDir := filepath.Join(dir, "python-traces")
			if mode == "retry-exception" {
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Fatal(err)
				}
				shimDir := filepath.Join(dir, "bin")
				for _, path := range []string{traceDir, shimDir} {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeReleaseFile(t, filepath.Join(shimDir, "python3"), []byte(`#!/usr/bin/env bash
set -uo pipefail
if [ "$1" = --version ]; then exec "$REAL_PYTHON" "$@"; fi
trace=$(mktemp "$PYTHON_TRACES/grade.XXXXXX") || exit 74
"$REAL_PYTHON" "$@" > "$trace"
status=$?
cat "$trace" || exit 74
exit "$status"
`))
				env = append(env, "REAL_PYTHON="+python, "PYTHON_TRACES="+traceDir,
					"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			captured := filepath.Join(dir, "captured.json")
			statusPath := filepath.Join(dir, "cli-status")
			launcher := filepath.Join(dir, "launch-real-cli")
			writeReleaseFile(t, launcher, []byte(releaseCaptureLauncher))
			env = append(env, "REAL_WAZA="+binary, "CAPTURE_STATUS="+statusPath,
				"CAPTURE_OUTCOME="+captured, "CAPTURE_PID="+filepath.Join(dir, "cli.pid"))
			if mode == "invalid-regex" || mode == "task-error" || strings.HasPrefix(mode, "hook-") {
				// Preserve production evidence even when admission rejects
				// the declarations before starting execution.
				cmd := exec.CommandContext(t.Context(), launcher, "run", "./examples/code-explainer/eval.yaml",
					"--verbose", "--output", filepath.Join(dir, "direct-invalid.json"))
				cmd.Dir, cmd.Env = dir, env
				directWant := 1
				if mode == "task-error" {
					directWant = 2
				} else if strings.HasPrefix(mode, "hook-") {
					directWant = 0
				}
				directOutput, directErr := cmd.CombinedOutput()
				if (directErr == nil) != (directWant == 0) || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != directWant {
					t.Fatalf("direct production control: %v\n%s", directErr, directOutput)
				}
				if strings.HasPrefix(mode, "hook-") {
					hook := strings.ReplaceAll(strings.TrimPrefix(mode, "hook-"), "-", "_")
					if !strings.Contains(string(directOutput), "[WARN] "+hook+" hook error") ||
						!strings.Contains(string(directOutput), "command exited with code 1") {
						t.Fatalf("failing hook was not exercised:\n%s", directOutput)
					}
				}
			}
			code, output := runReleaseGate(t, gate, launcher, dir, env)
			wantGate, wantCLI := 0, "0"
			if mode != "passed" && mode != "retry-exception" && mode != "retry-unused" && !strings.HasPrefix(mode, "hook-") {
				wantCLI = "1"
			}
			if mode == "task-error" {
				wantCLI = "2"
			}
			if mode == "task-error" || mode == "grader-exception" || mode == "invalid-regex" ||
				strings.HasPrefix(mode, "checkpoint-") || strings.HasPrefix(mode, "hook-") ||
				mode == "retry-exception" || mode == "missing-program" {
				wantGate = 1
			}
			status, err := os.ReadFile(statusPath)
			if err != nil || string(status) != wantCLI {
				t.Fatalf("production CLI exit=%q, want=%s: %v\n%s", status, wantCLI, err, output)
			}
			outcome, err := os.ReadFile(captured)
			if err != nil {
				t.Fatal(err)
			}
			var observed struct {
				Summary struct {
					TotalTests int `json:"total_tests"`
					Failed     int `json:"failed"`
					Errors     int `json:"errors"`
				} `json:"summary"`
				Tasks []struct {
					Status string `json:"status"`
					Runs   []struct {
						Status   string `json:"status"`
						ErrorMsg string `json:"error_msg"`
					} `json:"runs"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal(outcome, &observed); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "hook-") && code == 1 && !strings.Contains(string(output), `"hooks.execution"`) {
				t.Fatalf("hook admission did not identify unresolved hook execution:\n%s", output)
			}
			if mode == "retry-exception" || mode == "retry-unused" || mode == "missing-program" {
				filter := `[.tasks[].runs[] | select(.attempts == 1)] | length == 12`
				switch mode {
				case "retry-exception":
					filter = `.summary.succeeded == 4 and
						([.tasks[] | select(.test_id == "explain-python-recursion-001") | .runs[] |
						select(.attempts == 2 and .status == "passed" and
						.validations.explains_recursion.passed == true and
						(.validations.explains_recursion.details | has("evaluation_errors") | not))] | length == 1)`
				case "missing-program":
					filter = `[.tasks[].runs[].validations.has_explanation |
						select(.type == "program" and .passed == false and .score == 0 and
						(.details.exit_error | type == "string" and length > 0) and
						(.details | has("evaluation_errors") | not))] | length == 12`
				}
				check := exec.CommandContext(t.Context(), "jq", "-e", filter)
				check.Stdin = bytes.NewReader(outcome)
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("admission evidence not exercised: %v\n%s\n%s", err, output, outcome)
				}
			}
			if mode == "retry-exception" {
				traces, err := os.ReadDir(traceDir)
				if err != nil {
					t.Fatal(err)
				}
				exceptions := 0
				for _, trace := range traces {
					data, err := os.ReadFile(filepath.Join(traceDir, trace.Name()))
					if err != nil {
						t.Fatal(err)
					}
					var evidence struct {
						Results          []string          `json:"results"`
						EvaluationErrors map[string]string `json:"evaluation_errors"`
					}
					if err := json.Unmarshal(data, &evidence); err != nil {
						t.Fatalf("decoding actual Python output: %v\n%s", err, data)
					}
					if len(evidence.EvaluationErrors) > 0 {
						if len(evidence.EvaluationErrors) != 1 || evidence.EvaluationErrors["0"] != "division by zero" ||
							len(evidence.Results) == 0 || evidence.Results[0] != "division by zero" {
							t.Fatalf("unexpected first-attempt evidence: %s", data)
						}
						exceptions++
						t.Logf("actual discarded first-attempt exception: %s", data)
					}
				}
				if exceptions != 1 {
					t.Fatalf("expected one actual first-attempt evaluation exception, got %d from %d traces", exceptions, len(traces))
				}
			}
			if mode == "grader-exception" {
				check := exec.CommandContext(t.Context(), "jq", "-e", `
					[.tasks[] | select(.test_id == "explain-python-recursion-001") |
					.runs[].validations.explains_recursion |
					select(.passed == false and .score == 0 and
					(.details.evaluation_errors | type == "array" and length == 1))] | length == 3`)
				check.Stdin = bytes.NewReader(outcome)
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("exception evidence not exercised: %v\n%s\n%s", err, output, outcome)
				}
			}
			if strings.HasPrefix(mode, "checkpoint-") {
				check := exec.CommandContext(t.Context(), "jq", "-e", "--arg", "mode", mode, `
						[.tasks[] | select(.test_id == "explain-python-recursion-001") | .runs[] |
						select(.status == "failed" and .validations.explains_recursion.passed == false and
						(.validations.explains_recursion.details | has("evaluation_errors") | not) and
						(.checkpoints | length == 1) and .checkpoints[0].after_turn == 1 and
						(if $mode == "checkpoint-exception" then
						  .checkpoints[0].status == "failed" and
						  (.checkpoints[0].validations.intermediate.details.evaluation_errors | length == 1)
						 else .checkpoints[0].status == "passed" end))] | length == 3`)
				check.Stdin = bytes.NewReader(outcome)
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("checkpoint control not exercised: %v\n%s\n%s", err, output, outcome)
				}
			}
			if mode == "invalid-regex" || mode == "valid-regex-failure" {
				check := exec.CommandContext(t.Context(), "jq", "-e", "--arg", "mode", mode, `
						[.tasks[].runs[].validations.no_errors |
						select(.passed == false and .score == 0 and
						(if $mode == "invalid-regex" then
						  (.details.evaluation_errors | type == "array" and length == 1)
						 else (.details | has("evaluation_errors") | not) end))] | length == 12`)
				check.Stdin = bytes.NewReader(outcome)
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("regex control not exercised: %v\n%s\n%s", err, output, outcome)
				}
			}
			runs, errors, erroredTasks, failedTasks := 0, 0, 0, 0
			for _, task := range observed.Tasks {
				switch task.Status {
				case "error":
					erroredTasks++
				case "failed":
					failedTasks++
				}
				for _, run := range task.Runs {
					runs++
					if run.Status == "error" {
						errors++
						if task.Status != "error" {
							t.Fatalf("errored production run has task status %q", task.Status)
						}
						if !strings.Contains(run.ErrorMsg, "missing-release-instruction.md") {
							t.Fatalf("wrong production error: %q", run.ErrorMsg)
						}
					}
				}
			}
			wantErrors := 0
			wantErroredTasks := 0
			if mode == "task-error" {
				wantErrors = 3
				wantErroredTasks = 1
			}
			if len(observed.Tasks) != 4 || runs != 12 || errors != wantErrors || erroredTasks != wantErroredTasks ||
				(mode == "task-error" && failedTasks != 0) {
				t.Fatalf("production control not exercised: tasks=%d runs=%d errors=%d erroredTasks=%d failedTasks=%d\n%s",
					len(observed.Tasks), runs, errors, erroredTasks, failedTasks, outcome)
			}
			if observed.Summary.TotalTests != 4 || observed.Summary.Errors != wantErroredTasks ||
				observed.Summary.Failed != failedTasks ||
				(observed.Summary.Failed > 0) != (wantCLI == "1") {
				t.Fatalf("unexpected production summary: %+v", observed.Summary)
			}
			assertNoReleaseRuntime(t, guard)
			t.Logf("real CLI %s: CLI exit=%s gate exit=%d; tasks=4 trials=12 run errors=%d; no runtime launch", mode, status, code, errors)
			logReleaseOutcome(t, mode, outcome)
			if code != wantGate {
				t.Fatalf("gate exit=%d, want=%d\n%s", code, wantGate, output)
			}
			switch mode {
			case "passed":
				passing = outcome
			case "grader-failure":
				failing = outcome
			}
		})
	}
	if len(passing) == 0 || len(failing) == 0 {
		t.Fatal("production pass/failure controls must produce evidence before mutation tests")
	}
	t.Run("artifact-and-transport-controls", func(t *testing.T) {
		dir := t.TempDir()
		env, guard := releaseControlEnvironment(t, dir, root)
		source := filepath.Join(dir, "source.json")
		launcher := filepath.Join(dir, "replay")
		writeReleaseFile(t, launcher, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = preflight ]; then
  printf '%s\n' '{"kind":"waza.preflight","schemaVersion":"1.0","complete":true,"diagnostics":[]}'
  exit 0
fi
if [ "${OMIT_OUTCOME:-false}" != true ]; then cp "$REPLAY_OUTCOME" "$5"; fi
exit "$REPLAY_STATUS"
`))
		env = append(env, "REPLAY_OUTCOME="+source)
		for _, tc := range []struct {
			name, filter string
			status, want int
			failure      bool
		}{
			{"real-pass", ".", 0, 0, false},
			{"real-failure", ".", 1, 0, true},
			{"reordered", ".tasks |= reverse | .tasks[].runs |= reverse", 0, 0, false},
			{"failed-run-with-passing-task", ".tasks[0].runs[0].status = \"failed\"", 0, 1, false},
			{"failure-disguised-as-pass", ".", 0, 1, true},
			{"pass-disguised-as-failure", ".", 1, 1, false},
			{"task-errors", ".summary.errors = 1", 1, 1, true},
			{"missing-errors", "del(.summary.errors)", 0, 1, false},
			{"null-errors", ".summary.errors = null", 0, 1, false},
			{"string-errors", ".summary.errors = \"0\"", 0, 1, false},
			{"missing-skips", "del(.summary.skipped)", 0, 1, false},
			{"skipped", ".summary.skipped = 1", 0, 1, false},
			{"missing-summary", "del(.summary)", 0, 1, false},
			{"wrong-summary", ".summary.succeeded = 3", 0, 1, false},
			{"missing-task", "del(.tasks[0])", 0, 1, false},
			{"duplicate-task", ".tasks[0] = .tasks[1]", 0, 1, false},
			{"near-miss-task-id", ".tasks[0].test_id += \"-near-miss\"", 0, 1, false},
			{"unknown-task-status", ".tasks[0].status = \"complete\"", 0, 1, false},
			{"skipped-task", ".tasks[0].status = \"skipped\"", 0, 1, false},
			{"missing-run", "del(.tasks[0].runs[0])", 0, 1, false},
			{"null-runs", ".tasks[0].runs = null", 0, 1, false},
			{"duplicate-run", ".tasks[0].runs[0].run_number = 2", 0, 1, false},
			{"missing-attempts", "del(.tasks[0].runs[0].attempts)", 0, 1, false},
			{"null-attempts", ".tasks[0].runs[0].attempts = null", 0, 1, false},
			{"zero-attempts", ".tasks[0].runs[0].attempts = 0", 0, 1, false},
			{"retried-run", ".tasks[0].runs[0].attempts = 2", 0, 1, false},
			{"string-attempts", ".tasks[0].runs[0].attempts = \"1\"", 0, 1, false},
			{"error-run-with-clean-summary", ".tasks[0].runs[0].status = \"error\"", 0, 1, false},
			{"skipped-run", ".tasks[0].runs[0].status = \"skipped\"", 0, 1, false},
			{"error-message", ".tasks[0].runs[0].error_msg = \"runtime failed\"", 0, 1, false},
			{"null-error-message", ".tasks[0].runs[0].error_msg = null", 0, 1, false},
			{"empty-validations", ".tasks[0].runs[0].validations = {}", 0, 1, false},
			{"null-validations", ".tasks[0].runs[0].validations = null", 0, 1, false},
			{"missing-global-grader", "del(.tasks[0].runs[0].validations.no_errors)", 0, 1, false},
			{"missing-task-grader", "del(.tasks[0].runs[0].validations.explains_async_pattern)", 0, 1, false},
			{"missing-expectation-grader", "del(.tasks[0].runs[0].validations._output_contains)", 0, 1, false},
			{"wrong-grader-identity", ".tasks[0].runs[0].validations.no_errors.identifier = \"other\"", 0, 1, false},
			{"missing-grader-type", "del(.tasks[0].runs[0].validations.has_explanation.type)", 0, 1, false},
			{"null-grader-type", ".tasks[0].runs[0].validations.no_errors.type = null", 0, 1, false},
			{"wrong-code-grader-type", ".tasks[0].runs[0].validations.has_explanation.type = \"program\"", 0, 1, false},
			{"wrong-text-grader-type", ".tasks[0].runs[0].validations.no_errors.type = \"code\"", 0, 1, false},
			{"wrong-task-grader-type", ".tasks[0].runs[0].validations.explains_async_pattern.type = \"text\"", 0, 1, false},
			{"wrong-expectation-grader-type", ".tasks[0].runs[0].validations._output_contains.type = \"code\"", 0, 1, false},
			{"missing-score", "del(.tasks[0].runs[0].validations.no_errors.score)", 0, 1, false},
			{"invalid-score", ".tasks[0].runs[0].validations.no_errors.score = \"1\"", 0, 1, false},
			{"passed-with-zero-score", ".tasks[0].runs[0].validations.no_errors.score = 0", 0, 1, false},
			{"passed-with-partial-score", ".tasks[0].runs[0].validations.no_errors.score = 0.5", 0, 1, false},
			{"failed-with-perfect-score", "(.tasks[].runs[].validations[] | select(.passed == false)).score = 1", 1, 1, true},
			{"failed-with-partial-score", "(.tasks[].runs[].validations[] | select(.passed == false)).score = 0.5", 1, 0, true},
			{"empty-checkpoints", ".tasks[].runs[].checkpoints = []", 0, 0, false},
			{"null-checkpoints", ".tasks[0].runs[0].checkpoints = null", 0, 1, false},
			{"grader-failure-with-passed-status", ".tasks[0].runs[0].validations.no_errors.passed = false", 0, 1, false},
			{"invalid-grader-result", ".tasks[0].runs[0].validations.no_errors.passed = \"true\"", 0, 1, false},
			{"cached", ".tasks[0].cached = true", 0, 1, false},
			{"null-cached", ".tasks[0].cached = null", 0, 1, false},
			{"wrong-engine", ".config.engine_type = \"copilot-sdk\"", 0, 1, false},
			{"wrong-trials", ".config.runs_per_test = 2", 0, 1, false},
			{"missing-identity", "del(.eval_id)", 0, 1, false},
			{"wrong-eval", ".eval_name += \"-near-miss\"", 0, 1, false},
			{"empty-object", "{}", 0, 1, false},
			{"null", "null", 0, 1, false},
			{"multiple-documents", "., .", 0, 1, false},
			{"transport-error-with-valid-evidence", ".", 2, 2, false},
			{"wrapper-error-with-valid-failure", ".", 74, 74, true},
			{"cancellation-with-valid-evidence", ".", 130, 130, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				input := passing
				if tc.failure {
					input = failing
				}
				mutate := exec.CommandContext(t.Context(), "jq", tc.filter)
				mutate.Stdin = bytes.NewReader(input)
				output, err := mutate.CombinedOutput()
				if err != nil {
					t.Fatalf("mutating production outcome: %v\n%s", err, output)
				}
				writeReleaseFile(t, source, output)
				code, log := runReleaseGate(t, gate, launcher, dir, append(env, fmt.Sprintf("REPLAY_STATUS=%d", tc.status)))
				if code != tc.want {
					t.Fatalf("gate exit=%d, want=%d\n%s", code, tc.want, log)
				}
				assertNoReleaseRuntime(t, guard)
			})
		}
		for _, malformed := range []string{"", "{", "not-json"} {
			writeReleaseFile(t, source, []byte(malformed))
			code, output := runReleaseGate(t, gate, launcher, dir, append(env, "REPLAY_STATUS=0"))
			if code != 1 {
				t.Fatalf("malformed evidence %q: gate exit=%d\n%s", malformed, code, output)
			}
		}
		// A valid artifact available elsewhere cannot replace a fresh missing output.
		writeReleaseFile(t, source, passing)
		code, output := runReleaseGate(t, gate, launcher, dir, append(env, "REPLAY_STATUS=0", "OMIT_OUTCOME=true"))
		if code != 1 {
			t.Fatalf("missing fresh evidence: gate exit=%d\n%s", code, output)
		}
	})
	t.Run("production-grader-collisions", func(t *testing.T) {
		for _, mode := range []string{"task", "eval", "eval-task", "expectation"} {
			t.Run(mode, func(t *testing.T) {
				dir := t.TempDir()
				example := filepath.Join(dir, "examples", "code-explainer")
				if err := os.MkdirAll(filepath.Dir(example), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(example, os.DirFS(filepath.Join(root, "examples", "code-explainer"))); err != nil {
					t.Fatal(err)
				}
				task := filepath.Join(example, "tasks", "explain-python-recursion.yaml")
				data, err := os.ReadFile(task)
				if err != nil {
					t.Fatal(err)
				}
				var declaration string
				switch mode {
				case "task":
					original := string(data)
					data = []byte(strings.Replace(original, `"len(output) > 10"`, `"1 / 0"`, 1))
					if original == string(data) {
						t.Fatal("duplicate control did not change the actual assertion")
					}
					declaration = "\n  - name: explains_recursion\n    type: code\n    config:\n      assertions: ['True']\n"
				case "eval", "eval-task":
					evalPath := filepath.Join(example, "eval.yaml")
					source, err := os.ReadFile(evalPath)
					if err != nil {
						t.Fatal(err)
					}
					changed := strings.Replace(string(source), `"len(output) > 10"`, `"1 / 0"`, 1)
					if changed == string(source) {
						t.Fatal("eval collision control did not change the actual assertion")
					}
					declaration = "\n  - name: has_explanation\n    type: code\n    config:\n      assertions: ['True']\n"
					if mode == "eval" {
						withDuplicate := strings.Replace(changed, "\n# Task files", declaration+"\n# Task files", 1)
						if withDuplicate == changed {
							t.Fatal("eval duplicate control did not append the passing grader")
						}
						writeReleaseFile(t, evalPath, []byte(withDuplicate))
						declaration = ""
						break
					}
					writeReleaseFile(t, evalPath, []byte(changed))
					tasks, err := filepath.Glob(filepath.Join(example, "tasks", "*.yaml"))
					if err != nil {
						t.Fatal(err)
					}
					if len(tasks) != 4 {
						t.Fatalf("cross-scope control needs all four tasks, got %d", len(tasks))
					}
					for _, path := range tasks {
						if path == task {
							continue
						}
						source, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						writeReleaseFile(t, path, append(source, []byte(declaration)...))
					}
				case "expectation":
					declaration = "\n  - name: _output_contains\n    type: code\n    config:\n      assertions: ['1 / 0']\n"
				}
				writeReleaseFile(t, task, append(data, []byte(declaration)...))
				env, guard := releaseControlEnvironment(t, dir, root)
				direct := filepath.Join(dir, "direct.json")
				cmd := exec.CommandContext(t.Context(), binary, "run", "./examples/code-explainer/eval.yaml", "--output", direct)
				cmd.Dir, cmd.Env = dir, env
				// The production loader intentionally retains legacy name
				// overwrites. The release gate must reject these declarations.
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("production collision run: %v\n%s", err, output)
				}
				check := exec.CommandContext(t.Context(), "jq", "-e", `
					.summary.succeeded == 4 and .summary.failed == 0 and (.tasks | length == 4) and
					([.tasks[] | select(.test_id == "explain-python-recursion-001") | .runs[] |
					select(.status == "passed" and (.validations | length == 4) and
					all(.validations[]; .passed and (.details | has("evaluation_errors") | not)))] | length == 3)`, direct)
				if output, err := check.CombinedOutput(); err != nil {
					t.Fatalf("production overwrite not exercised: %v\n%s", err, output)
				}
				launcher := filepath.Join(dir, "gate-launcher")
				writeReleaseFile(t, launcher, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = run ]; then : > "$RUN_STARTED"; fi
exec "$REAL_WAZA" "$@"
`))
				started := filepath.Join(dir, "run-started")
				code, output := runReleaseGate(t, gate, launcher, dir,
					append(env, "REAL_WAZA="+binary, "RUN_STARTED="+started))
				if code != 1 {
					t.Fatalf("collision gate exit=%d, want=1\n%s", code, output)
				}
				diagnostic := "grader.result_collision"
				if mode == "task" || mode == "eval" {
					diagnostic = "grader.name"
				}
				if !strings.Contains(string(output), `"`+diagnostic+`"`) {
					t.Fatalf("collision admission did not identify %s\n%s", diagnostic, output)
				}
				if _, err := os.Stat(started); !os.IsNotExist(err) {
					t.Fatalf("colliding graders reached runtime execution: %v", err)
				}
				assertNoReleaseRuntime(t, guard)
			})
		}
	})
	t.Run("production-cancellation", func(t *testing.T) {
		dir := t.TempDir()
		example := filepath.Join(dir, "examples", "code-explainer")
		if err := os.MkdirAll(filepath.Dir(example), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(example, os.DirFS(filepath.Join(root, "examples", "code-explainer"))); err != nil {
			t.Fatal(err)
		}
		env, guard := releaseControlEnvironment(t, dir, root)
		barrier := filepath.Join(dir, "grader-active")
		shimDir := filepath.Join(dir, "bin")
		if err := os.Mkdir(shimDir, 0o700); err != nil {
			t.Fatal(err)
		}
		testBinary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		writeReleaseFile(t, filepath.Join(shimDir, "python3"), []byte(`#!/usr/bin/env bash
exec "$BLOCKING_GRADER_BINARY" -test.run=TestReleaseBlockingGrader -- "$@"
`))
		env = append(env, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BLOCKING_GRADER_BINARY="+testBinary, "BLOCKING_GRADER_BARRIER="+barrier)
		pidPath := filepath.Join(dir, "cli.pid")
		launcher := filepath.Join(dir, "cancel-real-cli")
		writeReleaseFile(t, launcher, []byte(releaseCaptureLauncher))
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", gate, launcher)
		cmd.Dir = dir
		statusPath := filepath.Join(dir, "cli-status")
		captured := filepath.Join(dir, "captured.json")
		cmd.Env = append(env, "REAL_WAZA="+binary, "CAPTURE_PID="+pidPath,
			"CAPTURE_STATUS="+statusPath, "CAPTURE_OUTCOME="+captured)
		cmd.WaitDelay = 3 * time.Second
		cmd.Cancel = func() error {
			data, err := os.ReadFile(pidPath)
			if err == nil {
				pid, parseErr := strconv.Atoi(string(data))
				if parseErr != nil {
					return parseErr
				}
				process, findErr := os.FindProcess(pid)
				if findErr != nil {
					return findErr
				}
				return process.Signal(syscall.SIGTERM)
			}
			if !os.IsNotExist(err) {
				return err
			}
			return cmd.Process.Kill()
		}
		var stderr, stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cmd.ProcessState == nil {
				cancel()
				_ = cmd.Wait()
			}
		}()
		for {
			if _, err := os.Stat(barrier); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
				t.Fatal("CLI never entered the blocking grader")
			case <-time.After(10 * time.Millisecond):
			}
		}
		data, err := os.ReadFile(pidPath)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cmd.ProcessState == nil {
				_ = process.Kill()
			}
		}()
		if err := process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		err = cmd.Wait()
		status, readErr := os.ReadFile(statusPath)
		if err == nil || ctx.Err() != nil || readErr != nil ||
			(string(status) != "1" && string(status) != "2") ||
			!strings.Contains(stderr.String(), "Release integration ") {
			t.Fatalf("production cancellation: wait=%v ctx=%v\n%s\n%s",
				err, ctx.Err(), &stdout, &stderr)
		}
		if string(status) == "1" {
			// Some cancellation paths complete with exit 1 and save partial
			// results; they must not be confused with completed grader failures.
			if _, err := os.Stat(captured); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stderr.String(), "lacks a complete error-free mock outcome matching exit code 1") {
				t.Fatalf("cancellation artifact was not rejected:\n%s", &stderr)
			}
			outcome, err := os.ReadFile(captured)
			if err != nil {
				t.Fatal(err)
			}
			logReleaseOutcome(t, "cancellation", outcome)
		}
		assertNoReleaseRuntime(t, guard)
		t.Logf("SIGTERM reached active production CLI; CLI exit=%s rejected by gate; no runtime launch", status)
	})
	t.Run("production-forbidden-runtime", func(t *testing.T) {
		dir := t.TempDir()
		example := filepath.Join(dir, "examples", "code-explainer")
		if err := os.MkdirAll(filepath.Dir(example), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(example, os.DirFS(filepath.Join(root, "examples", "code-explainer"))); err != nil {
			t.Fatal(err)
		}
		evalPath := filepath.Join(example, "eval.yaml")
		source, err := os.ReadFile(evalPath)
		if err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(string(source), "executor: mock", "executor: copilot-sdk", 1)
		if changed == string(source) {
			t.Fatal("forbidden launch control did not change the executor")
		}
		writeReleaseFile(t, evalPath, []byte(changed))
		env, guard := releaseControlEnvironment(t, dir, root)
		code, output := runReleaseGate(t, gate, binary, dir, env)
		if code != 1 || !strings.Contains(string(output), "Release integration attempted to launch the Copilot runtime.") {
			t.Fatalf("production forbidden launch exit=%d\n%s", code, output)
		}
		if _, err := os.Stat(guard + ".invoked"); err != nil {
			t.Fatalf("production runtime did not reach the rejecting executable: %v", err)
		}
		t.Log("real copilot-sdk initialization reached the proven absolute rejecting guard; gate rejected launch; no live runtime")
	})
}

func TestReleaseBlockingGrader(t *testing.T) {
	barrier := os.Getenv("BLOCKING_GRADER_BARRIER")
	if barrier == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		os.Exit(0)
	}
	// The production inline grader is awaiting this subprocess. It cannot
	// finish before SIGTERM; without cancellation the parent's deadline fails.
	if err := os.WriteFile(barrier, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}

func logReleaseOutcome(t *testing.T, name string, outcome []byte) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, outcome); err != nil {
		t.Fatal(err)
	}
	t.Logf("production outcome %s: %s", name, &compact)
}

func releaseControlEnvironment(t *testing.T, dir, root string) ([]string, string) {
	t.Helper()
	for _, name := range []string{"home", "tmp", "cache", "config", "runner"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(dir, "home"),
		"TMPDIR=" + filepath.Join(dir, "tmp"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "RUNNER_TEMP=" + filepath.Join(dir, "runner"),
		"WAZA_NO_UPDATE_CHECK=1", "WAZA_MCP_RUNTIME_TEST=false",
		"WAZA_EVIDENCE_JUDGE_RUNTIME_TEST=false", "WAZA_RESPONDER_RUNTIME_TEST=false",
	}
	install := exec.CommandContext(t.Context(), "bash", filepath.Join(root, ".github", "scripts", "go_offline_env.sh"))
	install.Env = env
	var stderr bytes.Buffer
	install.Stderr = &stderr
	output, err := install.Output()
	if err != nil {
		t.Fatalf("installing rejecting guard: %v\n%s", err, &stderr)
	}
	guard := ""
	for _, assignment := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		env = append(env, assignment)
		if value, ok := strings.CutPrefix(assignment, "COPILOT_CLI_PATH="); ok {
			guard = value
		}
	}
	if !filepath.IsAbs(guard) {
		t.Fatal("runtime guard is not absolute")
	}
	assertNoReleaseRuntime(t, guard)
	return env, guard
}

func assertNoReleaseRuntime(t *testing.T, guard string) {
	t.Helper()
	if _, err := os.Stat(guard + ".invoked"); !os.IsNotExist(err) {
		t.Fatalf("forbidden runtime launch marker: %v", err)
	}
}

func writeReleaseFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func runReleaseGate(t *testing.T, gate, binary, dir string, env []string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", gate, binary)
	cmd.Dir, cmd.Env = dir, env
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil || cmd.ProcessState == nil {
		t.Fatalf("release gate did not complete: %v\n%s", err, output)
	}
	return cmd.ProcessState.ExitCode(), output
}
