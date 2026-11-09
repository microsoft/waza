package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type privateCaptureRootEngine struct {
	*execution.MockEngine
	root     string
	response *execution.ExecutionResponse
}

func (e *privateCaptureRootEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	request := *req
	request.WorkspaceDir = e.root
	resp, err := e.MockEngine.Execute(ctx, &request)
	e.response = resp
	return resp, err
}

func TestSavedRunCaptureDiagnosticsExcludePrivateRoot(t *testing.T) {
	privateRoot := filepath.Join(t.TempDir(), "private-home-sentinel", "missing-workspace")
	engine := &privateCaptureRootEngine{MockEngine: execution.NewMockEngine("test"), root: privateRoot}
	require.NoError(t, engine.Initialize(t.Context()))
	runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{
		Config: models.Config{TimeoutSec: 10},
	}, config.WithSpecDir(t.TempDir())), engine)
	result := runner.executeAttemptWithRequest(t.Context(), &models.TestCase{Stimulus: models.TaskStimulus{Message: "ordinary authored message"}}, 1, 1, nil, false)
	run := result.run
	require.ErrorIs(t, result.cause, os.ErrNotExist)
	require.Equal(t, models.StatusError, run.Status)
	require.NotNil(t, engine.response)
	require.ErrorIs(t, engine.response.CaptureFailure(), os.ErrNotExist)
	require.Contains(t, run.ErrorMsg, "workspace capture incomplete")
	require.NotContains(t, run.ErrorMsg, privateRoot)
	require.NotContains(t, run.ErrorMsg, "private-home-sentinel")
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	var saved models.RunResult
	destination := filepath.Join(t.TempDir(), "outcome.json")
	require.NoError(t, os.WriteFile(destination, raw, 0o600))
	persisted, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(persisted, &saved))
	require.Equal(t, run.ErrorMsg, saved.ErrorMsg)
	require.NotContains(t, saved.ErrorMsg, "private-home-sentinel")
	require.Contains(t, saved.FinalOutput, "ordinary authored message")
	require.Empty(t, saved.Validations)
	// WorkspaceDir and authored content are outside the generated diagnostic policy.
	require.Contains(t, string(raw), "private-home-sentinel")
	require.Nil(t, engine.response.WorkspaceFiles)
	require.NoError(t, engine.Shutdown(context.Background()))
	_, err = os.Stat(privateRoot)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLaterTurnCaptureDiagnosticCauseIsRetained(t *testing.T) {
	engine := execution.NewMockEngine("test")
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
	response := &execution.ExecutionResponse{
		WorkspaceFiles: map[string][]byte{"stale.txt": []byte("stale")},
		JudgeFiles:     &models.JudgeFileCapture{},
	}
	turn, err := engine.Execute(t.Context(), &execution.ExecutionRequest{
		WorkspaceDir: filepath.Join(t.TempDir(), "private-home-sentinel"), Message: "observed turn",
	})
	require.NoError(t, err)
	require.ErrorIs(t, turn.CaptureFailure(), os.ErrNotExist)
	require.True(t, mergeLaterTurnResponse(response, turn, os.ErrPermission))
	require.ErrorIs(t, response.CaptureFailure(), os.ErrNotExist)
	require.Nil(t, response.WorkspaceFiles)
	require.Nil(t, response.JudgeFiles)
	require.Equal(t, turn.FinalOutput, response.FinalOutput)
	require.NotContains(t, response.CaptureFailure().Error(), "private-home-sentinel")
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-home-sentinel")
}
