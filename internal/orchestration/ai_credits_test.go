package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type gradingUsageEngine struct {
	execution.AgentEngine
	fail  bool
	calls int
}

func (e *gradingUsageEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	resp := &execution.ExecutionResponse{Success: true, SessionID: "task", FinalOutput: "answer", Usage: &models.UsageStats{AICredits: utils.Ptr(1.0)}}
	if req.EphemeralSession {
		resp.SessionID = fmt.Sprintf("judge-%d", e.calls)
		resp.Usage.AICredits = utils.Ptr(2.0)
		if e.fail {
			return resp, errors.New("judge failed")
		}
		_, err := req.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"description": "pass"}})
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func TestRunRecordsFinalAndCheckpointGraderUsage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			engine := &gradingUsageEngine{fail: fail}
			spec := &models.EvalSpec{Config: models.Config{TrialsPerTask: 1, TimeoutSec: 30}}
			runner := NewEvalRunner(config.NewEvalConfig(spec), engine)
			grader := models.ValidatorInline{Identifier: "judge", Kind: models.GraderKindPrompt, Parameters: models.PromptGraderParameters{Prompt: "grade"}}
			tc := &models.TestCase{
				TestID: "task", Stimulus: models.TaskStimulus{Message: "answer"},
				Validators:  []models.ValidatorInline{grader},
				Checkpoints: []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{grader}}},
			}
			run := runner.executeRun(t.Context(), tc, 1)
			require.Equal(t, "task", run.SessionDigest.SessionID)
			require.Len(t, run.GraderSessions, 2)
			require.Equal(t, 2.0, *run.GraderSessions[0].Usage.AICredits)
			require.Equal(t, 2.0, *run.GraderSessions[1].Usage.AICredits)
			if fail {
				require.Equal(t, models.StatusError, run.Status)
			} else {
				require.Equal(t, models.StatusPassed, run.Status)
			}
			usage := aggregateUsageFromOutcomes([]models.TestOutcome{{Runs: []models.RunResult{run}}})
			require.Equal(t, 5.0, *usage.AICredits)
		})
	}
}
