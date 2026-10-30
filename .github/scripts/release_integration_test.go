package scripts

import (
	"bufio"
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
	for _, mode := range []string{"passed", "grader-failure", "task-error"} {
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
			case "grader-failure":
				original := string(data)
				data = []byte(strings.Replace(original, `"len(output) > 10"`, `"len(output) < 0"`, 1))
				if original == string(data) {
					t.Fatal("grader-failure control did not change the actual task assertion")
				}
			case "task-error":
				data = append(data, []byte("\ninstruction_files:\n  - missing-release-instruction.md\n")...)
			}
			writeReleaseFile(t, task, data)
			env, guard := releaseControlEnvironment(t, dir, root)
			captured := filepath.Join(dir, "captured.json")
			statusPath := filepath.Join(dir, "cli-status")
			launcher := filepath.Join(dir, "launch-real-cli")
			writeReleaseFile(t, launcher, []byte(releaseCaptureLauncher))
			env = append(env, "REAL_WAZA="+binary, "CAPTURE_STATUS="+statusPath,
				"CAPTURE_OUTCOME="+captured, "CAPTURE_PID="+filepath.Join(dir, "cli.pid"))
			code, output := runReleaseGate(t, gate, launcher, dir, env)
			wantGate, wantCLI := 0, "0"
			if mode != "passed" {
				wantCLI = "1"
			}
			if mode == "task-error" {
				wantGate = 1
			}
			if code != wantGate {
				t.Fatalf("gate exit=%d, want=%d\n%s", code, wantGate, output)
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
					Runs []struct {
						Status   string `json:"status"`
						ErrorMsg string `json:"error_msg"`
					} `json:"runs"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal(outcome, &observed); err != nil {
				t.Fatal(err)
			}
			runs, errors := 0, 0
			for _, task := range observed.Tasks {
				for _, run := range task.Runs {
					runs++
					if run.Status == "error" {
						errors++
						if !strings.Contains(run.ErrorMsg, "missing-release-instruction.md") {
							t.Fatalf("wrong production error: %q", run.ErrorMsg)
						}
					}
				}
			}
			wantErrors := 0
			if mode == "task-error" {
				wantErrors = 3
			}
			if len(observed.Tasks) != 4 || runs != 12 || errors != wantErrors {
				t.Fatalf("production control not exercised: tasks=%d runs=%d errors=%d\n%s", len(observed.Tasks), runs, errors, outcome)
			}
			// Production currently aggregates run errors as failed tasks, with
			// summary.errors still zero. The gate must inspect the runs too.
			if observed.Summary.TotalTests != 4 || observed.Summary.Errors != 0 ||
				(observed.Summary.Failed > 0) != (mode != "passed") {
				t.Fatalf("unexpected production summary: %+v", observed.Summary)
			}
			assertNoReleaseRuntime(t, guard)
			t.Logf("real CLI %s: CLI exit=%s gate exit=%d; tasks=4 trials=12 run errors=%d; no runtime launch", mode, status, code, errors)
			logReleaseOutcome(t, mode, outcome)
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
			{"missing-score", "del(.tasks[0].runs[0].validations.no_errors.score)", 0, 1, false},
			{"invalid-score", ".tasks[0].runs[0].validations.no_errors.score = \"1\"", 0, 1, false},
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
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr, stdout bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		signaled := false
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(&stdout, line)
			if !signaled && strings.HasPrefix(line, "Running benchmark:") {
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
				if err := process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				signaled = true
			}
		}
		scanErr := scanner.Err()
		err = cmd.Wait()
		status, readErr := os.ReadFile(statusPath)
		if !signaled || scanErr != nil || err == nil || ctx.Err() != nil || readErr != nil ||
			(string(status) != "1" && string(status) != "2") ||
			!strings.Contains(stderr.String(), "Release integration ") {
			t.Fatalf("production cancellation: signaled=%t scan=%v wait=%v ctx=%v\n%s\n%s",
				signaled, scanErr, err, ctx.Err(), &stdout, &stderr)
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
