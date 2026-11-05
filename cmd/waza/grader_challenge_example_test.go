package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGradeRecordedChallengeExample(t *testing.T) {
	root := testutil.RepoFile(t, "examples", "grader-challenges", "cli-target")
	for _, candidate := range []struct {
		name          string
		passed        bool
		outcomePass   bool
		outcomeScore  float64
		boundaryPass  bool
		boundaryScore float64
		score         float64
	}{
		{"good", true, true, 1, true, 1, 1},
		{"alternative-valid", true, true, 1, true, 1, 1},
		{"bad-wrong-state", false, false, 1.0 / 3, true, 1, 2.0 / 3},
		{"bad-forbidden-action", false, true, 1, false, 0, 0.5},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			input := filepath.Join(root, "candidates", candidate.name+".json")
			workspace := filepath.Join(root, "workspaces", candidate.name)
			statePath := filepath.Join(workspace, "target.state")
			inputBefore, err := os.ReadFile(input)
			require.NoError(t, err)
			stateBefore, err := os.ReadFile(statePath)
			require.NoError(t, err)
			outputPath := filepath.Join(t.TempDir(), "graded.json")
			stdout, err := executeGrade(t, filepath.Join(root, "grade.yaml"),
				"--results", input, "--workspace", workspace, "--output", outputPath)
			require.NoError(t, err, "legacy grade keeps a successful exit for negative verdicts")
			var summary models.GradeOutcome
			require.NoError(t, json.Unmarshal([]byte(stdout), &summary))
			require.Equal(t, candidate.passed, summary.Passed)
			require.InDelta(t, candidate.score, summary.OverallScore, 1e-12)
			require.Len(t, summary.Tasks, 1)
			require.Equal(t, candidate.passed, summary.Tasks["cli-config-target"].Passed)
			saved, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			artifact, err := models.ParseEvaluationOutcome(saved, outputPath)
			require.NoError(t, err)
			require.Len(t, artifact.TestOutcomes, 1)
			task := artifact.TestOutcomes[0]
			require.Equal(t, "cli-config-target", task.TestID)
			status := models.StatusFailed
			if candidate.passed {
				status = models.StatusPassed
			}
			require.Equal(t, status, task.Status)
			require.Len(t, task.Runs, 1)
			run := task.Runs[0]
			require.Equal(t, status, run.Status)
			require.Equal(t, "Successfully completed the requested change.", run.FinalOutput)
			require.Len(t, run.Validations, 2)
			outcome := run.Validations["outcome"]
			boundary := run.Validations["boundary"]
			require.Equal(t, candidate.outcomePass, outcome.Passed)
			require.InDelta(t, candidate.outcomeScore, outcome.Score, 1e-12)
			require.Equal(t, candidate.boundaryPass, boundary.Passed)
			require.InDelta(t, candidate.boundaryScore, boundary.Score, 1e-12)
			if candidate.outcomePass {
				require.Equal(t, "All file checks passed", outcome.Feedback)
			} else {
				require.Contains(t, outcome.Feedback, "missing expected pattern")
				require.Contains(t, outcome.Feedback, "contains forbidden pattern")
			}
			if candidate.boundaryPass {
				require.Equal(t, "all tool_calls checks passed", boundary.Feedback)
			} else {
				require.Contains(t, boundary.Feedback, `forbidden tool "config_reset" was called`)
			}
			inputAfter, err := os.ReadFile(input)
			require.NoError(t, err)
			stateAfter, err := os.ReadFile(statePath)
			require.NoError(t, err)
			require.Equal(t, inputBefore, inputAfter)
			require.Equal(t, stateBefore, stateAfter)
			entries, err := os.ReadDir(workspace)
			require.NoError(t, err)
			require.Len(t, entries, 1, "no evaluator-only configuration or candidate JSON in workspace")
			require.Equal(t, "target.state", entries[0].Name())
		})
	}
}
