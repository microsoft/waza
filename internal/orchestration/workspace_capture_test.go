package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

func TestLaterTurnCaptureFailurePreservesCompletedObservations(t *testing.T) {
	for _, mode := range []struct {
		name       string
		responder  bool
		hasMessage bool
	}{
		{name: "follow_up", hasMessage: true},
		{name: "responder", responder: true, hasMessage: true},
		{name: "follow_up_without_message"},
		{name: "responder_without_message", responder: true},
	} {
		for _, cumulative := range []bool{false, true} {
			t.Run(mode.name+map[bool]string{false: "/per_turn", true: "/cumulative"}[cumulative], func(t *testing.T) {
				workspace := t.TempDir()
				contextDir := t.TempDir()
				snapshot := filepath.Join(contextDir, "expected.txt")
				require.NoError(t, os.WriteFile(snapshot, []byte("first output"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "output.txt"), []byte("restored output"), 0o600))
				captureErr := "workspace capture incomplete: file exceeds per-file limit"
				firstUsage := &models.UsageStats{Turns: 1, InputTokens: 10, OutputTokens: 2}
				lastUsage := &models.UsageStats{Turns: 1, InputTokens: 20, OutputTokens: 3}
				if cumulative {
					lastUsage = &models.UsageStats{Turns: 2, InputTokens: 30, OutputTokens: 5}
				}
				first := &execution.ExecutionResponse{
					Success: true, SessionID: "session", WorkspaceDir: workspace,
					WorkspaceFiles: map[string][]byte{"output.txt": []byte("first output")},
					FinalOutput:    "first output", DurationMs: 100, Usage: firstUsage, UsageIsCumulative: cumulative,
					Events: copilotevents.FromSDK([]copilot.SessionEvent{{
						Data: &copilot.AssistantMessageData{Content: "first output"},
					}}),
					ToolCalls:        []models.ToolCall{{ID: "first", Name: "view", Success: true}},
					SkillInvocations: []execution.SkillInvocation{{Name: "first-skill", Path: "/skills/first"}},
				}
				last := &execution.ExecutionResponse{
					Success: false, SessionID: "session", WorkspaceDir: workspace,
					ErrorMsg: captureErr, WorkspaceCaptureError: captureErr,
					FinalOutput: "completed second output", DurationMs: 200, Usage: lastUsage, UsageIsCumulative: cumulative,
					Events: copilotevents.FromSDK([]copilot.SessionEvent{
						{Data: &copilot.ToolExecutionStartData{ToolCallID: "last", ToolName: "edit", Arguments: map[string]any{"path": "output.txt"}}},
						{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "last", Success: true}},
						{Data: &copilot.AssistantMessageData{Content: "completed second output"}},
					}),
					ToolCalls:        []models.ToolCall{{ID: "last", Name: "edit", Success: true}},
					SkillInvocations: []execution.SkillInvocation{{Name: "last-skill", Path: "/skills/last"}},
				}
				if !mode.hasMessage {
					last.ErrorMsg = ""
				}
				engine := &workspaceCaptureSequenceEngine{responses: []*execution.ExecutionResponse{first, last}}
				diff := models.DiffGraderParameters{
					ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "output.txt", Snapshot: "expected.txt"}},
					ContextDir:    contextDir,
				}
				spec := &models.EvalSpec{
					Config:  models.Config{TimeoutSec: 30},
					Graders: []models.GraderConfig{{Kind: models.GraderKindDiff, Identifier: "final", Parameters: diff}},
				}
				runner := NewEvalRunner(config.NewEvalConfig(spec), engine, WithUpdateSnapshots(true))
				stimulus := models.TaskStimulus{Message: "write output", FollowUps: []string{"continue", "must not run"}}
				if mode.responder {
					stimulus.FollowUps = nil
					stimulus.Responder = &models.ResponderConfig{Instructions: "provide defaults", MaxFollowups: 2}
					runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
						return &scriptedClassifier{decisions: []responder.Decision{{Kind: responder.DecisionReply, Answer: "continue"}}}
					}
				}
				run := runner.executeRun(t.Context(), &models.TestCase{
					Stimulus: stimulus,
					Checkpoints: []models.Checkpoint{
						{AfterTurn: 1, Graders: []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "checkpoint", Parameters: diff}}},
						{AfterTurn: 2, Graders: []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "must-not-run", Parameters: diff}}},
					},
				}, 1)
				raw, err := json.Marshal(run)
				require.NoError(t, err)
				var saved models.RunResult
				require.NoError(t, json.Unmarshal(raw, &saved))
				require.Equal(t, models.StatusError, saved.Status)
				require.Contains(t, saved.ErrorMsg, captureErr)
				require.Equal(t, "completed second output", saved.FinalOutput)
				require.EqualValues(t, 300, saved.DurationMs)
				require.Equal(t, &models.UsageStats{Turns: 2, InputTokens: 30, OutputTokens: 5}, saved.SessionDigest.Usage)
				require.Equal(t, []models.ToolCall{
					{ID: "first", Name: "view", Success: true}, {ID: "last", Name: "edit", Success: true},
				}, saved.SessionDigest.ToolCalls)
				require.Len(t, saved.Transcript, 4)
				content, ok := copilotevents.Content(saved.Transcript[3].SessionEvent)
				require.True(t, ok)
				require.Equal(t, "completed second output", content)
				require.Len(t, saved.ToolEvents, 1)
				require.Equal(t, "last", saved.ToolEvents[0].ToolCallID)
				require.Equal(t, []models.SkillInvocation{
					{Name: "first-skill", Path: "/skills/first"}, {Name: "last-skill", Path: "/skills/last"},
				}, saved.SkillInvocations)
				require.Empty(t, saved.Validations)
				require.Len(t, saved.Checkpoints, 1)
				require.Equal(t, models.StatusPassed, saved.Checkpoints[0].Status)
				require.Empty(t, engine.responses)
				require.Nil(t, first.WorkspaceFiles, "the previous turn's snapshot must not survive a failed capture")
				snapshotContent, err := os.ReadFile(snapshot)
				require.NoError(t, err)
				require.Equal(t, "first output", string(snapshotContent))
			})
		}
	}
}

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
