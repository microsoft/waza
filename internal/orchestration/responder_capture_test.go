package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

type responderCaptureSession struct {
	t         *testing.T
	workspace string
	sendErr   error
	oversized bool
	calls     int
	handlers  []copilot.SessionEventHandler
}

func (s *responderCaptureSession) SessionID() string { return "capture-session" }
func (s *responderCaptureSession) Disconnect() error { return nil }
func (s *responderCaptureSession) On(handler copilot.SessionEventHandler) func() {
	s.handlers = append(s.handlers, handler)
	return func() {}
}
func (s *responderCaptureSession) UsageMetrics(context.Context) (*rpc.UsageGetMetricsResult, error) {
	return nil, errors.New("test runtime has no usage metrics")
}
func (s *responderCaptureSession) ShutdownUsage(context.Context) (*copilot.SessionShutdownData, error) {
	return nil, nil
}
func (s *responderCaptureSession) SendAndWait(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
	s.calls++
	output := "initial output"
	if s.calls == 2 {
		output = "second output"
		if s.oversized {
			file, err := os.Create(filepath.Join(s.workspace, "oversized.txt"))
			require.NoError(s.t, err)
			require.NoError(s.t, file.Truncate(16*1024*1024+1))
			require.NoError(s.t, file.Close())
		}
	}
	require.NoError(s.t, os.WriteFile(filepath.Join(s.workspace, "output.txt"), []byte(output), 0o600))
	for _, handler := range s.handlers {
		handler(copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: output}})
	}
	if s.calls == 2 {
		return nil, s.sendErr
	}
	return nil, nil
}

type responderCaptureClient struct {
	session *responderCaptureSession
}

func (c *responderCaptureClient) Start(context.Context) error { return nil }
func (c *responderCaptureClient) Stop() error                 { return nil }
func (c *responderCaptureClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return &copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil
}
func (c *responderCaptureClient) ListModels(context.Context) ([]copilot.ModelInfo, error) {
	return nil, nil
}
func (c *responderCaptureClient) DeleteSession(context.Context, string) error { return nil }
func (c *responderCaptureClient) CreateSession(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
	c.session.workspace = cfg.WorkingDirectory
	return c.session, nil
}
func (c *responderCaptureClient) ResumeSessionWithOptions(_ context.Context, _ string, cfg *copilot.ResumeSessionConfig) (execution.CopilotSession, error) {
	require.Equal(c.session.t, c.session.workspace, cfg.WorkingDirectory)
	return c.session, nil
}

type responderCaptureEngine struct {
	*execution.CopilotEngine
	t             *testing.T
	responses     []*execution.ExecutionResponse
	finalizations int
}

func (e *responderCaptureEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	resp, err := e.CopilotEngine.Execute(ctx, req)
	require.NoError(e.t, err, "the real engine reports send and capture failures in its response")
	e.responses = append(e.responses, resp)
	return resp, err
}

func (e *responderCaptureEngine) FinalizeCommandMocks(workspace string) ([]models.CommandInvocation, error) {
	e.finalizations++
	return e.CopilotEngine.FinalizeCommandMocks(workspace)
}

func TestResponderCaptureFailureCountsOnlySuccessfulExecution(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "completed"},
		{name: "failed", err: errors.New("synthetic execution failure")},
		{name: "timed_out", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled},
	} {
		for _, oversized := range []bool{false, true} {
			t.Run(failure.name+map[bool]string{false: "/captured", true: "/capture_failed"}[oversized], func(t *testing.T) {
				session := &responderCaptureSession{t: t, sendErr: failure.err, oversized: oversized}
				client := &responderCaptureClient{session: session}
				engine := &responderCaptureEngine{t: t, CopilotEngine: execution.NewCopilotEngineBuilder("test-model", &execution.CopilotEngineBuilderOptions{
					NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
				}).Build()}
				require.NoError(t, engine.Initialize(t.Context()))
				t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
				contextDir := t.TempDir()
				snapshot := filepath.Join(contextDir, "expected.txt")
				require.NoError(t, os.WriteFile(snapshot, []byte("initial output"), 0o600))
				diff := models.DiffGraderParameters{
					ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "output.txt", Snapshot: "expected.txt"}},
					ContextDir:    contextDir,
				}
				runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{
					Config:  models.Config{ModelID: "test-model", TimeoutSec: 30},
					Graders: []models.GraderConfig{{Kind: models.GraderKindDiff, Identifier: "final", Parameters: diff}},
				}, config.WithSpecDir(t.TempDir())), engine, WithUpdateSnapshots(true))
				runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
					return &scriptedClassifier{decisions: []responder.Decision{{Kind: responder.DecisionReply, Answer: "continue"}}}
				}
				run := runner.executeRun(t.Context(), &models.TestCase{
					Stimulus: models.TaskStimulus{
						Message: "execute", Responder: &models.ResponderConfig{Instructions: "provide defaults", MaxFollowups: 1},
					},
					Checkpoints: []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{
						{Kind: models.GraderKindDiff, Identifier: "initial", Parameters: diff},
					}}},
				}, 1)
				raw, err := json.Marshal(run)
				require.NoError(t, err)
				var saved models.RunResult
				require.NoError(t, json.Unmarshal(raw, &saved))
				require.Equal(t, 2, session.calls)
				require.Len(t, engine.responses, 2)
				last := engine.responses[1]
				require.Equal(t, failure.err == nil, last.ExecutionSucceeded)
				responseJSON, err := json.Marshal(last)
				require.NoError(t, err)
				require.NotContains(t, string(responseJSON), "ExecutionSucceeded")
				require.Equal(t, oversized, last.WorkspaceCaptureError != "")
				if oversized {
					info, err := os.Stat(filepath.Join(session.workspace, "oversized.txt"))
					require.NoError(t, err)
					require.EqualValues(t, 16*1024*1024+1, info.Size())
					require.Nil(t, last.WorkspaceFiles)
					require.Contains(t, saved.ErrorMsg, "workspace capture incomplete")
				}
				require.NotNil(t, saved.Responder)
				wantCount := 1
				if failure.err != nil {
					wantCount = 0
					require.Contains(t, last.ErrorMsg, failure.err.Error())
					require.Contains(t, saved.ErrorMsg, failure.err.Error())
				}
				require.Equal(t, wantCount, saved.Responder.FollowupsSent)
				require.Equal(t, 1, engine.finalizations)
				require.Len(t, saved.Checkpoints, 1)
				require.Equal(t, models.StatusPassed, saved.Checkpoints[0].Status)
				if oversized || failure.err != nil {
					require.Equal(t, models.StatusError, saved.Status)
					require.Equal(t, models.ResponderOutcomeError, saved.Responder.Outcome)
				}
				if oversized {
					require.Empty(t, saved.Validations)
					require.Equal(t, "second output", saved.FinalOutput)
					content, err := os.ReadFile(snapshot)
					require.NoError(t, err)
					require.Equal(t, "initial output", string(content))
				} else {
					require.Len(t, saved.Validations, 1, "preserve established non-capture grading behavior")
				}
			})
		}
	}
}
