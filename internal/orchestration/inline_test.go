package orchestration

import (
	"testing"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInlineProjectionRejectsMissingOrIncompleteObservation(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", ModelID: "mock", TrialsPerTask: 1, TimeoutSec: 30, DisabledSkills: []string{"*"}}}
	task := &models.TestCase{TestID: "task", Expectation: models.TaskExpectation{MustInclude: []string{"hello"}}}
	runner := NewEvalRunner(config.NewEvalConfig(spec), nil)
	run := &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed}
	usage := &models.EvaluationUsage{Sessions: []models.EvaluationSessionUsage{}}
	outcome, err := runner.ProjectInlineOutcome(task, run, time.Now(), usage)
	require.NoError(t, err, "control must satisfy the profile before testing missing observations")
	require.NotNil(t, outcome)
	for _, control := range []struct {
		name   string
		runner *EvalRunner
		task   *models.TestCase
		run    *models.RunResult
		usage  *models.EvaluationUsage
	}{
		{"nil runner", nil, task, run, usage},
		{"missing config", &EvalRunner{}, task, run, usage},
		{"nil task", runner, nil, run, usage},
		{"nil run", runner, task, nil, usage},
		{"missing usage scope", runner, task, run, nil},
		{"ungraded run", runner, task, &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusError}, usage},
		{"different trial", runner, task, &models.RunResult{RunNumber: 2, Attempts: 1, Status: models.StatusPassed}, usage},
		{"missing attempt", runner, task, &models.RunResult{RunNumber: 1, Status: models.StatusPassed}, usage},
		{"unknown status", runner, task, &models.RunResult{RunNumber: 1, Attempts: 1, Status: "unknown"}, usage},
	} {
		t.Run(control.name, func(t *testing.T) {
			outcome, err := control.runner.ProjectInlineOutcome(control.task, control.run, time.Now(), control.usage)
			require.Error(t, err)
			require.Nil(t, outcome)
		})
	}
}
