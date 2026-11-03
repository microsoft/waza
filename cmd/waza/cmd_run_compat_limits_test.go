package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func TestRunnerAdapterTerminalGraderLimits(t *testing.T) {
	for _, control := range []struct {
		name                 string
		evalCount, taskCount int
		expectations         bool
		identifier           string
		reject               string
	}{
		{name: "combined boundary", evalCount: 62, taskCount: 63, expectations: true},
		{name: "combined overflow", evalCount: 62, taskCount: 64, expectations: true, reject: "128 graders"},
		{name: "eval boundary", evalCount: 128},
		{name: "eval overflow", evalCount: 129, reject: "128 graders"},
		{name: "task boundary", taskCount: 128},
		{name: "task overflow", taskCount: 129, reject: "128 graders"},
		{name: "ASCII name boundary", evalCount: 1, identifier: strings.Repeat("a", 256)},
		{name: "ASCII name overflow", evalCount: 1, identifier: strings.Repeat("a", 257), reject: "256 UTF-8 bytes"},
		{name: "UTF-8 name boundary", taskCount: 1, identifier: strings.Repeat("é", 128)},
		{name: "UTF-8 name overflow", taskCount: 1, identifier: strings.Repeat("é", 129), reject: "256 UTF-8 bytes"},
	} {
		t.Run(control.name, func(t *testing.T) {
			evalPath, taskPath, eval, task := adapterCommandInputs(t)
			t.Chdir(filepath.Dir(evalPath))
			evalPrefix, _, ok := strings.Cut(string(eval), "graders:\n")
			require.True(t, ok)
			taskPrefix, _, ok := strings.Cut(string(task), "expected:\n")
			require.True(t, ok)
			appendGraders := func(prefix string, count int) string {
				if count == 0 {
					return ""
				}
				var source strings.Builder
				source.WriteString("graders:\n")
				for i := range count {
					name := fmt.Sprintf("%s-%d", prefix, i)
					if control.identifier != "" {
						name = control.identifier
					}
					fmt.Fprintf(&source, "  - name: %q\n    type: text\n    config:\n      contains: [hello]\n", name)
				}
				return source.String()
			}
			eval = []byte(evalPrefix + appendGraders("eval", control.evalCount) + "tasks: [\"tasks/selected.case.yaml\"]\n")
			task = []byte(taskPrefix + appendGraders("task", control.taskCount))
			if control.expectations {
				task = append(task, []byte("expected:\n  output_contains: [hello]\n  output_not_contains: [forbidden]\n  output_contains_any: [hello]\n")...)
			}
			require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
			require.NoError(t, os.WriteFile(taskPath, task, 0o600))
			var request bytes.Buffer
			adapt := newRootCommand()
			adapt.SetArgs(append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...))
			adapt.SetOut(&request)
			adapt.SetErr(io.Discard)
			err := adapt.Execute()
			if control.reject != "" {
				require.ErrorContains(t, err, control.reject)
				require.Empty(t, request.Bytes(), "invalid mappings must not emit a runnable request")
				output := filepath.Join(t.TempDir(), "forbidden.json")
				local := newRootCommand()
				local.SetArgs([]string{"run", evalPath, "--runner-compat", "--output", output})
				local.SetOut(io.Discard)
				local.SetErr(io.Discard)
				require.ErrorContains(t, local.Execute(), control.reject)
				_, err = os.Stat(output)
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			captured, err := runnercontract.Parse(request.Bytes())
			require.NoError(t, err)
			require.Equal(t, string(eval), captured.Input.Eval)
			require.Equal(t, string(task), captured.Input.Task)
			run := newRootCommand()
			run.SetArgs([]string{"runner", "run"})
			run.SetIn(bytes.NewReader(request.Bytes()))
			var output bytes.Buffer
			run.SetOut(&output)
			run.SetErr(io.Discard)
			require.NoError(t, run.Execute())
			var envelope runnerterminal.Envelope
			require.NoError(t, json.Unmarshal(output.Bytes(), &envelope))
			require.Equal(t, runnerterminal.Success, envelope.Outcome, output.String())
			require.Equal(t, 1, envelope.EngineCalls)
			require.NotNil(t, envelope.Observation)
			count := control.evalCount + control.taskCount
			if control.expectations {
				count += 3
			}
			require.Len(t, envelope.Observation.Validations, count)
			for _, verdict := range envelope.Observation.Validations {
				require.True(t, verdict.Passed, verdict.Identifier)
				if control.identifier != "" {
					require.Equal(t, control.identifier, verdict.Identifier)
				}
			}
		})
	}
}

func TestRunnerCompatExecutionTimeoutDoesNotLimitTextGrading(t *testing.T) {
	evalPath, taskPath, eval, task := adapterCommandInputs(t)
	t.Chdir(filepath.Dir(evalPath))
	require.Contains(t, string(eval), "timeout_seconds: 30")
	eval = bytes.Replace(eval, []byte("timeout_seconds: 30"), []byte("timeout_seconds: 1"), 1)
	require.Contains(t, string(eval), "Mock response for: hello")
	eval = bytes.Replace(eval, []byte("Mock response for: hello"), []byte("Mock response for:"), 1)
	require.Contains(t, string(task), "prompt: hello")
	task = bytes.Replace(task, []byte("prompt: hello"), []byte("prompt: "+strings.Repeat("a", 64<<10)+"hello"), 1)
	// Each full-output regex scan is a real builtin text check, not a sleep in
	// execution. Assert the measured grading duration actually crosses the limit.
	patterns := strings.Repeat(`"(?s)^.*$",`, 16384)
	// Replace the existing task grader rather than introducing a duplicate key.
	prefix, _, ok := strings.Cut(string(task), "graders:\n")
	require.True(t, ok)
	task = []byte(prefix + "graders:\n  - name: slow-text\n    type: text\n    config:\n      regex_match: [" + patterns + "]\n")
	require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
	require.NoError(t, os.WriteFile(taskPath, task, 0o600))
	for _, adapted := range []bool{false, true} {
		t.Run(fmt.Sprintf("compat=%t", adapted), func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			output := filepath.Join(t.TempDir(), "graded.json")
			args := []string{"run", evalPath, "--output", output, "--no-summary", "--no-update-check"}
			if adapted {
				args = append(args, "--runner-compat")
			}
			cmd := newRootCommand()
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			require.NoError(t, cmd.Execute())
			outcome, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.Equal(t, 1, outcome.Digest.Succeeded)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, models.StatusPassed, run.Status)
			require.Less(t, run.DurationMs, int64(1000), "mock execution must finish inside its one-second budget")
			graded, exists := run.Validations["slow-text"]
			require.True(t, exists)
			require.True(t, graded.Passed)
			require.GreaterOrEqual(t, graded.DurationMs, int64(1000), "fixture must exercise grading beyond the execution-only timeout")
			t.Logf("execution=%dms text-grading=%dms", run.DurationMs, graded.DurationMs)
		})
	}
	t.Run("external end-to-end deadline", func(t *testing.T) {
		adapt := newRootCommand()
		adapt.SetArgs(append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...))
		var request bytes.Buffer
		adapt.SetOut(&request)
		adapt.SetErr(io.Discard)
		require.NoError(t, adapt.Execute())
		run := newRootCommand()
		run.SetArgs([]string{"runner", "run"})
		run.SetIn(&request)
		var output bytes.Buffer
		run.SetOut(&output)
		run.SetErr(io.Discard)
		var exit *ExitCodeError
		require.ErrorAs(t, run.Execute(), &exit)
		require.Equal(t, 4, exit.Code)
		var envelope runnerterminal.Envelope
		require.NoError(t, json.Unmarshal(output.Bytes(), &envelope))
		require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
		require.Equal(t, 1, envelope.EngineCalls)
	})
}
