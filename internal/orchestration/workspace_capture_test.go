package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type workspaceCaptureFailureEngine struct {
	workspace string
}

func (e *workspaceCaptureFailureEngine) Initialize(context.Context) error { return nil }
func (e *workspaceCaptureFailureEngine) Shutdown(context.Context) error   { return nil }
func (e *workspaceCaptureFailureEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *workspaceCaptureFailureEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	message := "workspace capture incomplete: file exceeds per-file limit"
	return &execution.ExecutionResponse{
		Success: false, ErrorMsg: message, WorkspaceCaptureError: message,
		WorkspaceDir: e.workspace, FinalOutput: "done",
	}, nil
}

func TestWorkspaceCaptureFailureSkipsCheckpointsAndSnapshotUpdates(t *testing.T) {
	workspace := t.TempDir()
	contextDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "output.txt"), []byte("restored content"), 0o644))
	snapshotPath := filepath.Join(contextDir, "expected.txt")
	require.NoError(t, os.WriteFile(snapshotPath, []byte("original snapshot"), 0o644))

	diff := models.DiffGraderParameters{
		ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "output.txt", Snapshot: "expected.txt"}},
		ContextDir:    contextDir,
	}
	spec := &models.EvalSpec{
		Config:  models.Config{TimeoutSec: 30},
		Graders: []models.GraderConfig{{Kind: models.GraderKindDiff, Identifier: "final", Parameters: diff}},
	}
	runner := NewEvalRunner(config.NewEvalConfig(spec), &workspaceCaptureFailureEngine{workspace: workspace}, WithUpdateSnapshots(true))
	run := runner.executeRun(t.Context(), &models.TestCase{
		Stimulus: models.TaskStimulus{Message: "write output"},
		Checkpoints: []models.Checkpoint{{
			AfterTurn: 1,
			Graders:   []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "checkpoint", Parameters: diff}},
		}},
	}, 1)

	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, "workspace capture incomplete")
	require.Empty(t, run.Validations)
	require.Empty(t, run.Checkpoints)
	content, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Equal(t, "original snapshot", string(content))
}
