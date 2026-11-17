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
	executeErr  error
	finalized   bool
}

func (e *commandMockFinalizerTestEngine) Initialize(context.Context) error { return nil }
func (e *commandMockFinalizerTestEngine) Shutdown(context.Context) error   { return nil }
func (e *commandMockFinalizerTestEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *commandMockFinalizerTestEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if e.response == nil {
		e.response = &execution.ExecutionResponse{
			Success:      true,
			WorkspaceDir: "/workspace",
		}
	}
	return e.response, e.executeErr
}
func (e *commandMockFinalizerTestEngine) FinalizeCommandMocks(string) ([]models.CommandInvocation, error) {
	e.finalized = true
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

func TestWorkspaceCaptureFailureFinalizesCommandMocks(t *testing.T) {
	captureErr := "workspace capture incomplete: file exceeds per-file limit"
	engine := &commandMockFinalizerTestEngine{
		finalizeErr: errors.New("command mock expected 2 call(s), got 1"),
		response: &execution.ExecutionResponse{
			Success:               false,
			ErrorMsg:              captureErr,
			WorkspaceCaptureError: captureErr,
			WorkspaceDir:          "/workspace",
		},
	}

	runner := NewEvalRunner(
		config.NewEvalConfig(&models.EvalSpec{Config: models.Config{TimeoutSec: 30}}),
		engine,
		WithSkipGraders(),
	)
	run := runner.executeRun(context.Background(), &models.TestCase{
		Stimulus: models.TaskStimulus{Message: "run az"},
	}, 1)

	require.True(t, engine.finalized)
	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, captureErr)
	require.Contains(t, run.ErrorMsg, engine.finalizeErr.Error())
	require.NotNil(t, run.InfrastructureDiagnostic)
	require.True(t, run.InfrastructureDiagnostic.AdditionalFailure)
}

func TestWorkspaceCaptureAndExecutionFailuresAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name       string
		executed   bool
		executeErr error
		additional bool
	}{
		{name: "capture only", executed: true},
		{name: "execution and capture", additional: true},
		{name: "go error and capture", executed: true, executeErr: errors.New("private execution error"), additional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureErr := errors.New("capture /private/secret")
			engine := &commandMockFinalizerTestEngine{
				executeErr: tc.executeErr,
				response: &execution.ExecutionResponse{
					ExecutionSucceeded: tc.executed, WorkspaceCaptureError: captureErr.Error(),
					WorkspaceCaptureCause: captureErr, ErrorMsg: captureErr.Error(), WorkspaceDir: t.TempDir(),
				},
			}
			runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{Config: models.Config{TimeoutSec: 30}}),
				engine, WithSkipGraders())
			run := runner.executeRun(t.Context(), &models.TestCase{Stimulus: models.TaskStimulus{Message: "run"}}, 1)
			require.NotNil(t, run.InfrastructureDiagnostic)
			require.Equal(t, tc.additional, run.InfrastructureDiagnostic.AdditionalFailure)
			require.Equal(t, models.CaptureFailed, run.InfrastructureDiagnostic.Code)
		})
	}
}
