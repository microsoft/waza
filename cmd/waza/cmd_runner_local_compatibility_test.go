package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerLocalCommandFrozenOracle(t *testing.T) {
	const corpus = "../../internal/runnercontract/testdata/local"
	var oracle struct {
		TaskID        string        `json:"task_id"`
		TaskName      string        `json:"task_name"`
		Status        models.Status `json:"status"`
		RunNumber     int           `json:"run_number"`
		Attempts      int           `json:"attempts"`
		Check         string        `json:"check"`
		Passed        bool          `json:"passed"`
		Score         float64       `json:"score"`
		ResultVersion string        `json:"result_writer_version"`
		RunnerVersion string        `json:"runner_writer_version"`
	}

	data, err := os.ReadFile(filepath.Join(corpus, "oracle.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &oracle))
	task, err := os.ReadFile(filepath.Join(corpus, "task.yaml"))
	require.NoError(t, err)
	for _, version := range []string{"unversioned", "1.0", "1.4", "2.0"} {
		t.Run(version, func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			eval, err := os.ReadFile(filepath.Join(corpus, "eval-"+version+".yaml"))
			require.NoError(t, err)
			dir := isolatedCLITempDir(t)
			evalPath := filepath.Join(dir, "eval.yaml")
			taskPath := filepath.Join(dir, "task.yaml")
			require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
			require.NoError(t, os.WriteFile(taskPath, task, 0o600))
			output := filepath.Join(dir, "legacy.json")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--output", output, "--no-summary"})
			require.NoError(t, cmd.Execute())
			legacyData, err := os.ReadFile(output)
			require.NoError(t, err)
			legacy, err := models.ParseEvaluationOutcome(legacyData, output)
			require.NoError(t, err)
			require.Equal(t, oracle.ResultVersion, legacy.SchemaVersion)
			require.Len(t, legacy.TestOutcomes, 1)
			require.Equal(t, oracle.TaskID, legacy.TestOutcomes[0].TestID)
			require.Equal(t, oracle.TaskName, legacy.TestOutcomes[0].DisplayName)
			require.Len(t, legacy.TestOutcomes[0].Runs, 1)

			adapt := newRootCommand()
			adapt.SetArgs([]string{"runner", "adapt", "--eval", evalPath, "--task", taskPath,
				"--run-key", "frozen-local", "--shard", "local", "--task-id", oracle.TaskID, "--trial", "1", "--attempt", "1"})
			var request bytes.Buffer
			adapt.SetOut(&request)
			require.NoError(t, adapt.Execute())
			run := newRootCommand()
			run.SetArgs([]string{"runner", "run"})
			run.SetIn(&request)
			var terminal bytes.Buffer
			run.SetOut(&terminal)
			require.NoError(t, run.Execute())
			result, err := runnercontract.ParseResult(terminal.Bytes())
			require.NoError(t, err)
			require.Equal(t, runnercontract.Success, result.Outcome)
			require.Equal(t, oracle.RunnerVersion, result.SchemaVersion)
			for _, observation := range []*models.RunResult{&legacy.TestOutcomes[0].Runs[0], result.Observation} {
				require.NotNil(t, observation)
				require.Equal(t, oracle.Status, observation.Status)
				require.Equal(t, oracle.RunNumber, observation.RunNumber)
				require.Equal(t, oracle.Attempts, observation.Attempts)
				check, ok := observation.Validations[oracle.Check]
				require.True(t, ok)
				require.Equal(t, oracle.Passed, check.Passed)
				require.Equal(t, oracle.Score, check.Score)
			}
			_, err = models.ParseEvaluationOutcome(terminal.Bytes(), "unit.runner.json")
			require.ErrorContains(t, err, "not evaluation results")
		})
	}
}
