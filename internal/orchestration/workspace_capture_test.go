package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
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

type workspaceCaptureSequenceEngine struct {
	responses []*execution.ExecutionResponse
}

func (e *workspaceCaptureSequenceEngine) Initialize(context.Context) error { return nil }
func (e *workspaceCaptureSequenceEngine) Shutdown(context.Context) error   { return nil }
func (e *workspaceCaptureSequenceEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *workspaceCaptureSequenceEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	response := e.responses[0]
	e.responses = e.responses[1:]
	return response, nil
}

func TestResponderCaptureFailurePreservesCompletedResults(t *testing.T) {
	workspace := t.TempDir()
	contextDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "expected.txt"), []byte("captured content"), 0o644))

	captureErr := "workspace capture incomplete: file exceeds per-file limit"
	engine := &workspaceCaptureSequenceEngine{
		responses: []*execution.ExecutionResponse{
			{
				Success:        true,
				SessionID:      "session",
				WorkspaceDir:   workspace,
				WorkspaceFiles: map[string][]byte{"output.txt": []byte("captured content")},
				FinalOutput:    "Which configuration should I use?",
			},
			{
				Success:               false,
				ErrorMsg:              captureErr,
				WorkspaceCaptureError: captureErr,
				WorkspaceDir:          workspace,
			},
		},
	}
	diff := models.DiffGraderParameters{
		ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "output.txt", Snapshot: "expected.txt"}},
		ContextDir:    contextDir,
	}
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30}}
	runner := NewEvalRunner(config.NewEvalConfig(spec), engine)
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
		return &scriptedClassifier{decisions: []responder.Decision{{Kind: responder.DecisionReply, Answer: "use defaults"}}}
	}

	run := runner.executeRun(t.Context(), &models.TestCase{
		Stimulus: models.TaskStimulus{
			Message:   "write output",
			Responder: &models.ResponderConfig{Instructions: "provide defaults", MaxFollowups: 1},
		},
		Checkpoints: []models.Checkpoint{{
			AfterTurn: 1,
			Graders:   []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "checkpoint", Parameters: diff}},
		}},
	}, 1)

	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, captureErr)
	require.Len(t, run.Checkpoints, 1)
	require.Equal(t, models.StatusPassed, run.Checkpoints[0].Status)
	require.NotNil(t, run.Responder)
	require.Equal(t, models.ResponderOutcomeError, run.Responder.Outcome)
	require.Contains(t, run.Responder.Reason, captureErr)
}
