package main

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// TestCompletedGraderResultsSurviveLaterGraderErrorCLIProcess is the #1114
// regression: a later operational grader error must not discard the verdicts of
// graders that already completed, nor change the error status or exit policy.
func TestCompletedGraderResultsSurviveLaterGraderErrorCLIProcess(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	guard := classificationRuntimeGuard(t)
	const (
		passing = `  - name: passing
    type: text
    config: {contains: [CAPTURED_OUTPUT_CANARY]}
`
		failing = `  - name: failing
    type: text
    config: {contains: [IMPOSSIBLE_OUTPUT_CANARY]}
`
		broken = `  - name: broken
    type: file
    config: {must_exist: [../outside-workspace]}
`
		after = `  - name: after
    type: text
    config: {contains: [CAPTURED_OUTPUT_CANARY]}
`
	)
	for _, control := range []struct {
		name, evalGraders, taskGraders string
		status                         models.Status
		exit                           int
		completed                      []string
	}{
		{"earlier pass then later error", passing + broken + after, "", models.StatusError, 2, []string{"passing"}},
		{"earlier failure then later error", passing + failing + broken + after, "", models.StatusError, 2, []string{"failing", "passing"}},
		{"task-level later error", passing, failing + broken + after, models.StatusError, 2, []string{"failing", "passing"}},
		{"all complete with failure", passing + failing, "", models.StatusFailed, 1, []string{"failing", "passing"}},
		{"all complete passing", passing, "", models.StatusPassed, 0, []string{"passing"}},
	} {
		t.Run(control.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "synthetic"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "synthetic", "SKILL.md"),
				[]byte("---\nname: synthetic\ndescription: Synthetic partial grading fixture.\n---\nReturn the requested answer.\n"), 0600))
			spec := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(spec, []byte(`name: partial-grading
skill: synthetic
config: {executor: mock, model: mock-model, timeout_seconds: 30, trials_per_task: 2, max_attempts: 2, skill_directories: [skills/synthetic]}
tasks: [task.yaml]
graders:
`+control.evalGraders), 0600))
			task := "id: task\nname: Partial grading task\ninputs: {prompt: CAPTURED_OUTPUT_CANARY}\n"
			if control.taskGraders != "" {
				task += "graders:\n" + control.taskGraders
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0600))
			env := append(os.Environ(), "NO_COLOR=1", "ENABLE_COPILOT_TESTS=false",
				"COPILOT_BASE_URL=", "COPILOT_PROVIDER_BASE_URL=",
				"COPILOT_GITHUB_TOKEN=offline-synthetic-token", "COPILOT_CLI_PATH="+guard,
				"HOME="+dir, "USERPROFILE="+dir, "XDG_CACHE_HOME="+dir, "LOCALAPPDATA="+dir)

			output := filepath.Join(dir, "results.json")
			stdout, stderr, code := runPartialGradingCLI(t, binary, dir, env, "run", spec, "--output", output)
			require.Equal(t, control.exit, code, "stdout=%s stderr=%s", stdout, stderr)
			require.NoFileExists(t, guard+".invoked", "no provider/runtime execution is permitted")
			outcome, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
			assertPartialGradingOutcome(t, outcome, control.status, control.completed)
			if control.status == models.StatusError {
				require.Contains(t, stderr, "0 failed and 1 error(s)")
			}

			raw, err := os.ReadFile(output)
			require.NoError(t, err)
			var saved struct {
				Tasks []struct {
					Runs []struct {
						Validations map[string]json.RawMessage `json:"validations"`
					} `json:"runs"`
				} `json:"tasks"`
			}
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Len(t, saved.Tasks, 1)
			require.Len(t, saved.Tasks[0].Runs, 2)
			for _, run := range saved.Tasks[0].Runs {
				require.Equal(t, control.completed, slices.Sorted(maps.Keys(run.Validations)),
					"saved JSON must retain completed graders without blocked or synthesized entries")
			}

			regradedPath := filepath.Join(t.TempDir(), "regraded.json")
			stdout, stderr, code = runPartialGradingCLI(t, binary, dir, env, "grade", spec,
				"--results", output, "--workspace", dir, "--output", regradedPath)
			wantGradeExit := 0
			if control.status == models.StatusError {
				wantGradeExit = 2
			}
			require.Equal(t, wantGradeExit, code, "stdout=%s stderr=%s", stdout, stderr)
			require.NoFileExists(t, guard+".invoked")
			regraded, err := models.LoadEvaluationOutcome(regradedPath)
			require.NoError(t, err)
			if control.status == models.StatusError {
				// Legacy regrading does not re-evaluate recorded error runs, so it
				// reports no regraded verdicts rather than replaying recorded ones.
				require.Equal(t, 1, regraded.Digest.Errors)
				for _, run := range regraded.TestOutcomes[0].Runs {
					require.Equal(t, models.StatusError, run.Status)
					require.Empty(t, run.Validations)
				}
				return
			}
			assertPartialGradingOutcome(t, regraded, control.status, control.completed)
			for i, run := range regraded.TestOutcomes[0].Runs {
				original := outcome.TestOutcomes[0].Runs[i]
				for _, name := range control.completed {
					require.Equal(t, original.Validations[name].Passed, run.Validations[name].Passed)
					require.Equal(t, original.Validations[name].Score, run.Validations[name].Score)
					require.Equal(t, original.Validations[name].Feedback, run.Validations[name].Feedback)
				}
			}
		})
	}
}

func runPartialGradingCLI(t *testing.T, binary, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, append([]string{"--no-update-check"}, args...)...)
	command.Dir, command.Env = dir, env
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "stdout=%s stderr=%s", stdout.String(), stderr.String())
	return stdout.String(), stderr.String(), exit.ExitCode()
}

func assertPartialGradingOutcome(t *testing.T, outcome *models.EvaluationOutcome, status models.Status, completed []string) {
	t.Helper()
	require.Len(t, outcome.TestOutcomes, 1)
	task := outcome.TestOutcomes[0]
	require.Equal(t, status, task.Status)
	require.Len(t, task.Runs, 2)
	switch status {
	case models.StatusError:
		require.Equal(t, 1, outcome.Digest.Errors)
		require.Zero(t, outcome.Digest.Failed)
		require.Zero(t, outcome.Digest.Succeeded)
	case models.StatusFailed:
		require.Equal(t, 1, outcome.Digest.Failed)
		require.Zero(t, outcome.Digest.Errors)
	case models.StatusPassed:
		require.Equal(t, 1, outcome.Digest.Succeeded)
		require.Zero(t, outcome.Digest.Errors+outcome.Digest.Failed)
	}
	for _, run := range task.Runs {
		require.Equal(t, status, run.Status)
		require.Equal(t, completed, slices.Sorted(maps.Keys(run.Validations)))
		passing := run.Validations["passing"]
		require.True(t, passing.Passed)
		require.Equal(t, 1.0, passing.Score)
		require.NotEmpty(t, passing.Feedback)
		if failing, ok := run.Validations["failing"]; ok {
			require.False(t, failing.Passed, "a completed failure verdict must be preserved")
			require.Less(t, failing.Score, 1.0)
			require.NotEmpty(t, failing.Feedback)
		}
		if status == models.StatusError {
			require.Equal(t, 1, run.Attempts, "operational errors must not be retried")
			require.Contains(t, run.ErrorMsg, "running graders:")
			require.Contains(t, run.ErrorMsg, "broken")
		} else {
			require.Empty(t, run.ErrorMsg)
		}
	}
}

func TestGradeRunWithJudgeRetainsCompletedResultsWhenLaterGraderErrors(t *testing.T) {
	passing := models.GraderConfig{Identifier: "passing", Kind: models.GraderKindText,
		Parameters: models.TextGraderParameters{Contains: []string{"CAPTURED_OUTPUT_CANARY"}}}
	failing := models.GraderConfig{Identifier: "failing", Kind: models.GraderKindText,
		Parameters: models.TextGraderParameters{Contains: []string{"IMPOSSIBLE_OUTPUT_CANARY"}}}
	broken := models.GraderConfig{Identifier: "broken", Kind: models.GraderKindFile,
		Parameters: models.FileGraderParameters{MustExist: []string{"../outside-workspace"}}}
	for _, control := range []struct {
		name      string
		graders   []models.GraderConfig
		completed []string
		wantErr   bool
	}{
		{"earlier pass and failure then later error", []models.GraderConfig{passing, failing, broken}, []string{"failing", "passing"}, true},
		{"first grader error", []models.GraderConfig{broken, passing}, nil, true},
		{"all complete control", []models.GraderConfig{passing, failing}, []string{"failing", "passing"}, false},
	} {
		t.Run(control.name, func(t *testing.T) {
			spec := &models.EvalSpec{Graders: control.graders}
			task := &models.TestCase{TestID: "task"}
			run := &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed,
				FinalOutput: "CAPTURED_OUTPUT_CANARY", Validations: map[string]models.GraderResults{"stale": {Name: "stale", Passed: true}}}
			graded, err := gradeRunWithJudge(t.Context(), spec, task, run, t.TempDir(), "", io.Discard, false, evidenceGradeSource{})
			require.NotNil(t, graded)
			require.Equal(t, control.completed, slices.Sorted(maps.Keys(graded.Validations)))
			require.Contains(t, run.Validations, "stale", "the source run must not be mutated")
			if !control.wantErr {
				require.NoError(t, err)
				require.False(t, graded.Validations["failing"].Passed)
				return
			}
			require.ErrorContains(t, err, "broken")
			require.Equal(t, models.StatusError, graded.Status)
			require.Equal(t, err.Error(), graded.ErrorMsg)
			require.NotNil(t, graded.Validations, "error runs keep an empty, non-nil validation map")
			if len(control.completed) > 0 {
				require.True(t, graded.Validations["passing"].Passed)
				require.False(t, graded.Validations["failing"].Passed)
				require.NotEmpty(t, graded.Validations["failing"].Feedback)
			}
		})
	}
}
