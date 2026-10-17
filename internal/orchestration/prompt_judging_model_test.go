package orchestration

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEvidenceRunDefaultsJudgeToEvalModelWithoutActorReuse(t *testing.T) {
	for _, tc := range []struct{ name, judge, grader, want string }{
		{"eval fallback", "", "", "actor-model"},
		{"judge default", "judge-model", "", "judge-model"},
		{"grader override", "judge-model", "grader-model", "grader-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, actor := evidenceRunner(t)
			spec := runner.cfg.Spec()
			spec.Config.JudgeModel = tc.judge
			grader := evidenceConfig("judge")
			parameters, ok := grader.Parameters.(models.PromptGraderParameters)
			require.True(t, ok)
			parameters.Model = tc.grader
			grader.Parameters = parameters
			spec.Graders = []models.GraderConfig{grader}
			calls := 0
			runner.evidenceExecutor = scriptedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				require.Equal(t, tc.want, request.ModelID)
				require.Empty(t, request.SessionID)
				require.Empty(t, request.WorkspaceDir)
				_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
				return &execution.ExecutionResponse{Success: true, SessionID: "independent-judge", ModelID: request.ModelID}, err
			})
			run := runner.executeAttempt(t.Context(), &models.TestCase{
				TestID: "task", DisplayName: "Task", Stimulus: models.TaskStimulus{Message: "Produce a report"},
			}, 1, 1)
			require.Equal(t, models.StatusPassed, run.Status, run.ErrorMsg)
			require.Equal(t, 1, calls)
			require.Equal(t, int64(1), actor.Creates.Load())
			require.Zero(t, actor.Resumes.Load())
		})
	}
}
