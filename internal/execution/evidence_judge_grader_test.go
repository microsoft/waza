package execution_test

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type reasoningJudgeClient struct{ *execution.MockCopilotClient }

func (*reasoningJudgeClient) ForceStop() {}

func TestEvidenceGraderRejectsUnsupportedReasoningBeforeCreatingSession(t *testing.T) {
	supported := copilot.ModelInfo{ID: "judge", SupportedReasoningEfforts: []string{"low"}}
	supported.Capabilities.Supports.ReasoningEffort = true
	missing := supported
	missing.SupportedReasoningEfforts = nil
	for _, tc := range []struct {
		name, model string
		catalog     []copilot.ModelInfo
		catalogErr  error
	}{
		{"auto", "auto", nil, nil},
		{"unsupported effort", "judge", []copilot.ModelInfo{supported}, nil},
		{"unsupported model", "judge", []copilot.ModelInfo{{ID: "judge"}}, nil},
		{"missing metadata", "judge", []copilot.ModelInfo{missing}, nil},
		{"unknown model", "judge", nil, nil},
		{"catalog failure", "judge", nil, errors.New("CATALOG_PRIVATE_CANARY")},
		{"canceled catalog", "judge", nil, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"COPILOT_BASE_URL", "COPILOT_API_KEY", "COPILOT_PROVIDER_TYPE", "COPILOT_WIRE_API"} {
				t.Setenv(name, "")
			}
			t.Setenv("COPILOT_GITHUB_TOKEN", "synthetic-hosted-judge")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &reasoningJudgeClient{execution.NewMockCopilotClient(gomock.NewController(t))}
			client.EXPECT().Start(gomock.Any()).Return(nil)
			client.EXPECT().Stop().Return(nil)
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, *copilot.SessionConfig) (execution.CopilotSession, error) {
				t.Error("unsupported reasoning settings reached session creation")
				return nil, errors.New("unexpected session")
			}).AnyTimes()
			if tc.model != "auto" {
				client.EXPECT().ListModels(gomock.Any()).DoAndReturn(func(catalogCtx context.Context) ([]copilot.ModelInfo, error) {
					if errors.Is(tc.catalogErr, context.Canceled) {
						cancel()
						<-catalogCtx.Done()
					}
					return tc.catalog, tc.catalogErr
				})
			}
			origin := models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"}
			bundle := judgeevidence.Capture(judgeevidence.CaptureInput{
				Origin: origin, Task: "task", Output: "answer", OutputPresent: true, ExecutionMode: "mock",
				Events: []copilot.SessionEvent{{Data: &copilot.UserMessageData{Content: "task"}}, {Data: &copilot.AssistantMessageData{Content: "answer"}}},
			})
			require.NoError(t, judgeevidence.Verify(bundle, origin))
			grader, err := graders.Create("judge", models.PromptGraderParameters{
				Prompt: "Grade the frozen evidence.", Model: tc.model, ReasoningEffort: "max", EvidenceContext: &models.PromptEvidenceContext{},
			})
			require.NoError(t, err)
			var observations []models.PromptJudgeObservation
			result, err := grader.Grade(ctx, &graders.Context{
				JudgeEvidence: bundle, JudgeOrigin: origin, EvidenceExecutor: execution.EvidenceJudgeExecutorForTest(client),
				RecordPromptJudging: func(observation models.PromptJudgeObservation) { observations = append(observations, observation) },
			})
			require.ErrorContains(t, err, "reasoning effort")
			require.NotContains(t, err.Error(), "CATALOG_PRIVATE_CANARY")
			if tc.catalogErr != nil {
				require.ErrorIs(t, err, tc.catalogErr)
			}
			require.Nil(t, result)
			require.Len(t, observations, 1)
			require.Equal(t, "failed", observations[0].State)
			require.Empty(t, observations[0].SessionID)
			require.False(t, observations[0].SubmissionObserved)
			require.Nil(t, observations[0].Usage)
		})
	}
}
