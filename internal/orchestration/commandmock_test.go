package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type commandMockFinalizerTestEngine struct {
	response    *execution.ExecutionResponse
	finalizeErr error
}

func (e *commandMockFinalizerTestEngine) Initialize(context.Context) error { return nil }
func (e *commandMockFinalizerTestEngine) Shutdown(context.Context) error   { return nil }
func (e *commandMockFinalizerTestEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *commandMockFinalizerTestEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.response = &execution.ExecutionResponse{
		Success:      true,
		WorkspaceDir: "/workspace",
	}
	return e.response, nil
}
func (e *commandMockFinalizerTestEngine) FinalizeCommandMocks(string) ([]models.CommandInvocation, error) {
	return nil, e.finalizeErr
}

func TestCommandMockFinalizationErrorFailsRun(t *testing.T) {
	engine := &commandMockFinalizerTestEngine{finalizeErr: errors.New("command mock expected 2 call(s), got 1")}
	runner := NewEvalRunner(
		config.NewEvalConfig(&models.EvalSpec{Config: models.Config{TimeoutSec: 30}}),
		engine,
		WithSkipGraders(),
	)

	run := runner.executeRun(context.Background(), &models.TestCase{
		Stimulus: models.TaskStimulus{Message: "run az"},
	}, 1)

	require.False(t, engine.response.Success)
	require.Equal(t, models.StatusError, run.Status)
	require.Equal(t, engine.finalizeErr.Error(), run.ErrorMsg)
}
