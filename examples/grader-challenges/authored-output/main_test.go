package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestGeneratedExampleObservesActualNativeGraderButRemainsUnreviewed(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	require.NoError(t, generate(binary, directory, "."))
	labels, err := os.ReadFile(filepath.Join(directory, "labels.json"))
	require.NoError(t, err)
	references, err := assurance.ParseReferences(labels)
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "eval.yaml"))
	require.NoError(t, err)
	spec, err := models.LoadEvalSpec(filepath.Join(directory, "eval.yaml"))
	require.NoError(t, err)
	task, err := models.LoadTestCase(filepath.Join(directory, "task.yaml"))
	require.NoError(t, err)
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	report, err := assurance.Verify(t.Context(), assurance.VerifyRequest{
		References: references, Now: time.Now().UTC(), EvalSource: source, Spec: spec,
		Tasks: map[string]*models.TestCase{task.TestID: task}, SnapshotRoot: root,
	})
	require.NoError(t, err)
	require.Equal(t, assurance.AssessmentNotAssessed, report.State)
	require.False(t, report.Review.Eligible)
	require.Equal(t, "review_not_eligible", report.Requirements[0].Reason)
	require.Len(t, report.Requirements[0].Observations, 4)
	for index, observation := range report.Requirements[0].Observations {
		require.Equal(t, assurance.Observed, observation.State)
		require.Equal(t, index < 2, observation.Result.Passed)
		require.True(t, *observation.Agreement)
		require.NotEmpty(t, observation.Result.Feedback)
		require.Equal(t, "authored_finite_output", observation.SourceScope)
	}
	for _, binding := range report.Bindings {
		require.Equal(t, "verified", binding.State)
	}
	after, err := os.ReadFile(filepath.Join(directory, "labels.json"))
	require.NoError(t, err)
	require.Equal(t, labels, after)
	require.Zero(t, report.Calibration.Executions)
	require.Nil(t, report.Calibration.Credits)
}

func TestGeneratorRejectsInvalidInputsWithoutClobbering(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	require.Error(t, generate("", directory, "."))
	require.Error(t, generate(binary, "", "."))
	require.Error(t, generate(filepath.Join(directory, "missing"), directory, "."))
	require.Error(t, generate(binary, filepath.Join(directory, "missing"), "."))
	require.Error(t, generate(binary, directory, filepath.Join(directory, "missing")))
	path := filepath.Join(directory, "keep")
	require.NoError(t, os.WriteFile(path, []byte("untouched"), 0o600))
	require.Error(t, generate(binary, directory, "."))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(actual))
}

func TestGeneratorRejectsUnsupportedExampleDeclarationsBeforeWriting(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	eval, err := os.ReadFile("eval.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("task.yaml")
	require.NoError(t, err)
	for _, mutation := range []string{"empty-graders", "empty-requirements", "wrong-requirement", "wrong-grader"} {
		t.Run(mutation, func(t *testing.T) {
			source, directory := t.TempDir(), t.TempDir()
			spec, err := models.LoadEvalSpec("eval.yaml")
			require.NoError(t, err)
			example, err := models.LoadTestCase("task.yaml")
			require.NoError(t, err)
			evalBytes, taskBytes := eval, task
			switch mutation {
			case "empty-graders":
				start := strings.Index(string(eval), "graders:\n")
				end := strings.Index(string(eval), "\nmetrics:")
				require.GreaterOrEqual(t, start, 0)
				require.Greater(t, end, start)
				evalBytes = []byte(string(eval[:start]) + "graders: []" + string(eval[end:]))
			case "empty-requirements":
				taskBytes = []byte("id: finite-output\nname: Missing requirements\ninputs:\n  prompt: Return state\n")
			case "wrong-requirement":
				taskBytes = []byte(strings.ReplaceAll(string(task), example.Requirements[0].ID, "wrong"))
			case "wrong-grader":
				taskBytes = []byte(strings.ReplaceAll(string(task), "grader: "+spec.Graders[0].Identifier, "grader: missing"))
			}
			require.NoError(t, os.WriteFile(filepath.Join(source, "eval.yaml"), evalBytes, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(source, "task.yaml"), taskBytes, 0o600))
			require.ErrorContains(t, generate(binary, directory, source), "single eval grader")
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
