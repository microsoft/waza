package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

type workspaceCaptureRecordingEngine struct {
	*execution.MockEngine
	requests  []*execution.ExecutionRequest
	responses []*execution.ExecutionResponse
}

func (e *workspaceCaptureRecordingEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.requests = append(e.requests, req)
	resp, err := e.MockEngine.Execute(ctx, req)
	if resp != nil {
		e.responses = append(e.responses, resp)
	}
	return resp, err
}

func TestSkipGradersOmitsOnlyGraderWorkspaceCapturePaths(t *testing.T) {
	paths := []string{".git/task.txt", "node_modules/checkpoint.txt", "node_modules/global.txt"}
	oversized := strings.Repeat("x", 16*1024*1024+1)
	for _, mode := range []string{"regular", "missing", "directory", "oversized_grader_only", "oversized_workspace"} {
		for _, skip := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/graded", true: "/execution_only"}[skip], func(t *testing.T) {
				diff := func(path string) models.DiffGraderParameters {
					return models.DiffGraderParameters{
						ExpectedFiles: []models.DiffExpectedFileParameters{{Path: path, Contains: []string{"ready"}}},
					}
				}
				spec := &models.EvalSpec{
					Config:  models.Config{TimeoutSec: 30, ModelID: "mock-model"},
					Graders: []models.GraderConfig{{Kind: models.GraderKindDiff, Identifier: "global", Parameters: diff(paths[2])}},
				}
				tc := &models.TestCase{
					Stimulus: models.TaskStimulus{
						Message: "execute", FollowUps: []string{"continue"},
						Resources: []models.ResourceRef{{Location: "result.txt", Body: "independent output"}},
					},
					Validators: []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "task", Parameters: diff(paths[0])}},
					Checkpoints: []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{
						{Kind: models.GraderKindDiff, Identifier: "checkpoint", Parameters: diff(paths[1])},
					}}},
				}
				if mode != "missing" {
					for i, path := range paths {
						content := "ready"
						if mode == "directory" {
							path += "/nested.txt"
						}
						if mode == "oversized_grader_only" && i == 0 {
							content = oversized
						}
						tc.Stimulus.Resources = append(tc.Stimulus.Resources, models.ResourceRef{Location: path, Body: content})
					}
				}
				if mode == "oversized_workspace" {
					tc.Stimulus.Resources = append(tc.Stimulus.Resources, models.ResourceRef{Location: "large.txt", Body: oversized})
				}
				engine := &workspaceCaptureRecordingEngine{MockEngine: execution.NewMockEngine("mock-model")}
				require.NoError(t, engine.Initialize(t.Context()))
				t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
				runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine)
				if skip {
					WithSkipGraders()(runner)
				}
				run := runner.executeRun(t.Context(), tc, 1)
				captureFailure := mode == "oversized_workspace" || (!skip && (mode == "directory" || mode == "oversized_grader_only"))
				wantStatus := models.StatusPassed
				switch {
				case captureFailure:
					wantStatus = models.StatusError
				case skip:
					wantStatus = models.StatusSkipped
				case mode == "missing":
					wantStatus = models.StatusFailed
				}
				require.Equal(t, wantStatus, run.Status, run.ErrorMsg)
				wantTurns := 2
				if captureFailure {
					wantTurns = 1
					require.Contains(t, run.ErrorMsg, "workspace capture incomplete")
				}
				require.Len(t, engine.requests, wantTurns)
				require.Len(t, engine.responses, wantTurns)
				for i, req := range engine.requests {
					require.False(t, req.SkipWorkspaceCapture, "execution-only must retain ordinary workspace capture")
					if skip {
						require.Nil(t, req.WorkspaceCapturePaths)
					} else {
						require.Equal(t, paths, req.WorkspaceCapturePaths)
					}
					resp := engine.responses[i]
					if captureFailure {
						require.NotEmpty(t, resp.WorkspaceCaptureError)
						require.Nil(t, resp.WorkspaceFiles)
					} else {
						require.Empty(t, resp.WorkspaceCaptureError)
						require.Equal(t, []byte("independent output"), resp.WorkspaceFiles["result.txt"])
						for _, path := range paths {
							if !skip && mode == "regular" {
								require.Equal(t, []byte("ready"), resp.WorkspaceFiles[path])
							} else {
								require.NotContains(t, resp.WorkspaceFiles, path)
							}
						}
					}
				}
				if skip {
					require.Empty(t, run.Validations)
					require.Empty(t, run.Checkpoints)
				}
				info, err := os.Stat(filepath.Join(engine.responses[0].WorkspaceDir, filepath.FromSlash(paths[0])))
				switch mode {
				case "directory":
					require.NoError(t, err)
					require.True(t, info.IsDir(), "fixture must be a non-readable-as-file grader target")
				case "oversized_grader_only":
					require.NoError(t, err)
					require.EqualValues(t, len(oversized), info.Size())
				case "missing":
					require.True(t, os.IsNotExist(err))
				default:
					require.NoError(t, err)
				}
			})
		}
	}
}

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
				if mode.responder {
					require.NotNil(t, saved.Responder)
					require.Equal(t, 1, saved.Responder.FollowupsSent)
					require.Equal(t, models.ResponderOutcomeError, saved.Responder.Outcome)
				}
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
	workspace      string
	withoutMessage bool
	calls          int
	finalizations  int
	finalizeErr    error
}

func (e *workspaceCaptureFailureEngine) Initialize(context.Context) error { return nil }
func (e *workspaceCaptureFailureEngine) Shutdown(context.Context) error   { return nil }
func (e *workspaceCaptureFailureEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *workspaceCaptureFailureEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	message := "workspace capture incomplete: file exceeds per-file limit"
	errorMsg := message
	if e.withoutMessage {
		errorMsg = ""
	}
	return &execution.ExecutionResponse{
		Success: false, ErrorMsg: errorMsg, WorkspaceCaptureError: message,
		WorkspaceDir: e.workspace, FinalOutput: "done",
	}, nil
}

func (e *workspaceCaptureFailureEngine) FinalizeCommandMocks(string) ([]models.CommandInvocation, error) {
	e.finalizations++
	return nil, e.finalizeErr
}

func TestWorkspaceCaptureFailureSkipsCheckpointsAndSnapshotUpdates(t *testing.T) {
	for _, mode := range []struct {
		name           string
		withoutMessage bool
		skipGraders    bool
		finalizerError bool
	}{
		{name: "with_message"},
		{name: "without_message", withoutMessage: true},
		{name: "skip_graders", skipGraders: true},
		{name: "skip_graders_without_message", skipGraders: true, withoutMessage: true},
		{name: "finalizer_error", finalizerError: true},
		{name: "finalizer_error_without_message", finalizerError: true, withoutMessage: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
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
			engine := &workspaceCaptureFailureEngine{
				workspace: workspace, withoutMessage: mode.withoutMessage,
			}
			if mode.finalizerError {
				engine.finalizeErr = errors.New("command mock finalizer failed")
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec), engine, WithUpdateSnapshots(true))
			if mode.skipGraders {
				WithSkipGraders()(runner)
			}
			run := runner.executeRun(t.Context(), &models.TestCase{
				Stimulus: models.TaskStimulus{Message: "write output", FollowUps: []string{"must not run"}},
				Checkpoints: []models.Checkpoint{{
					AfterTurn: 1,
					Graders:   []models.ValidatorInline{{Kind: models.GraderKindDiff, Identifier: "checkpoint", Parameters: diff}},
				}},
			}, 1)

			require.Equal(t, models.StatusError, run.Status)
			require.Contains(t, run.ErrorMsg, "workspace capture incomplete")
			if mode.finalizerError {
				require.Contains(t, run.ErrorMsg, engine.finalizeErr.Error())
			}
			require.Equal(t, "done", run.FinalOutput)
			require.Equal(t, 1, engine.calls)
			require.Equal(t, 1, engine.finalizations)
			require.Empty(t, run.Validations)
			require.Empty(t, run.Checkpoints)
			content, err := os.ReadFile(snapshotPath)
			require.NoError(t, err)
			require.Equal(t, "original snapshot", string(content))
		})
	}
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
