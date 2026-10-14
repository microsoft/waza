package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type sessionAccountingEngine struct {
	execute       func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error)
	usage         map[string]*models.UsageStats
	shutdownCalls int
}

func (e *sessionAccountingEngine) Initialize(context.Context) error { return nil }
func (e *sessionAccountingEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return e.execute(req)
}
func (e *sessionAccountingEngine) Shutdown(context.Context) error {
	e.shutdownCalls++
	return nil
}
func (e *sessionAccountingEngine) SessionUsage(id string) *models.UsageStats { return e.usage[id] }

func sessionAccountUsage(tokens int, credits float64) *models.UsageStats {
	return &models.UsageStats{
		InputTokens: tokens, AICredits: utils.Ptr(credits),
		ModelMetrics: map[string]models.ModelUsage{"model": {InputTokens: tokens, AICredits: utils.Ptr(credits)}},
	}
}

func TestMultiTurnBehaviorGradingUsesCurrentCumulativeUsageBeforeShutdown(t *testing.T) {
	for _, path := range []string{"follow-ups", "responder"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			engine := &sessionAccountingEngine{}
			engine.execute = func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				tokens := 100
				if calls > 1 {
					tokens = 250
				}
				return &execution.ExecutionResponse{
					SessionID: "s", Success: true, FinalOutput: "answer",
					Usage: sessionAccountUsage(tokens, float64(calls)), UsageIsCumulative: true,
				}, nil
			}
			spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 30}}
			runner := NewEvalRunner(config.NewEvalConfig(spec), engine)
			behavior := models.ValidatorInline{
				Identifier: "budget", Kind: models.GraderKindBehavior, Parameters: models.BehaviorGraderParameters{MaxTokens: 300},
			}
			tc := &models.TestCase{
				TestID: "task", Stimulus: models.TaskStimulus{Message: "initial"}, Validators: []models.ValidatorInline{behavior},
				Checkpoints: []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{behavior}}},
			}
			if path == "follow-ups" {
				tc.Stimulus.FollowUps = []string{"follow-up"}
			} else {
				tc.Stimulus.Responder = &models.ResponderConfig{Instructions: "reply", MaxFollowups: 1}
				runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
					return &scriptedClassifier{decisions: []responder.Decision{{Kind: responder.DecisionReply, Answer: "follow-up"}}}
				}
			}
			ctx, scope := execution.NewUsageScope(t.Context())
			run := runner.executeRun(ctx, tc, 1)
			require.Zero(t, engine.shutdownCalls, "grading must be correct before shutdown refresh")
			require.Equal(t, 2, calls)
			require.Equal(t, models.StatusPassed, run.Status)
			require.Equal(t, 250, run.SessionDigest.Usage.InputTokens)
			require.True(t, run.Validations["budget"].Passed)
			require.Len(t, run.Checkpoints, 1)
			require.Equal(t, models.StatusPassed, run.Checkpoints[0].Status)
			outcome := &models.EvaluationOutcome{EvaluationUsage: scope.Snapshot(), TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{run}}}}
			execution.UpdateOutcomeUsage(outcome, engine)
			require.Equal(t, 2.0, *outcome.Digest.Usage.AICredits)
		})
	}
}

func TestRetryAccountingIncludesDiscardedAttemptAndItsGraders(t *testing.T) {
	for _, continued := range []bool{false, true} {
		t.Run(fmt.Sprintf("continued=%t", continued), func(t *testing.T) {
			attempts := 0
			judgeCalls := 0
			engine := &sessionAccountingEngine{}
			engine.execute = func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				if len(req.Tools) == 0 {
					attempts++
					output := "bad"
					if attempts == 2 {
						output = "good"
					}
					return &execution.ExecutionResponse{
						SessionID: fmt.Sprintf("attempt-%d", attempts), Success: true, FinalOutput: output,
						Usage: sessionAccountUsage(10, 1), UsageIsCumulative: true,
					}, nil
				}
				judgeCalls++
				_, err := req.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"description": "pass"}})
				if err != nil {
					return nil, err
				}
				id, credits := fmt.Sprintf("judge-%d", judgeCalls), 2.0
				if continued {
					id = req.SessionID
					credits = 3 // cumulative task + grader
				}
				return &execution.ExecutionResponse{SessionID: id, Success: true, Usage: sessionAccountUsage(30, credits), UsageIsCumulative: true}, nil
			}
			spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 30, MaxAttempts: 2}}
			runner := NewEvalRunner(config.NewEvalConfig(spec), engine)
			tc := &models.TestCase{
				TestID: "retry", Stimulus: models.TaskStimulus{Message: "answer"},
				Validators: []models.ValidatorInline{
					{Identifier: "text", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"good"}}},
					{Identifier: "judge", Kind: models.GraderKindPrompt, Parameters: models.PromptGraderParameters{Prompt: "grade", ContinueSession: continued}},
				},
			}
			ctx, scope := execution.NewUsageScope(t.Context())
			task := runner.runTestUncached(ctx, tc, 1, 1)
			require.Equal(t, models.StatusPassed, task.Status)
			require.Len(t, task.Runs, 1)
			require.Equal(t, 2, task.Runs[0].Attempts)
			require.Equal(t, "attempt-2", task.Runs[0].SessionDigest.SessionID)
			require.Equal(t, 2, attempts)
			require.Equal(t, 2, judgeCalls)
			outcome := &models.EvaluationOutcome{EvaluationUsage: scope.Snapshot(), TestOutcomes: []models.TestOutcome{task}}
			execution.UpdateOutcomeUsage(outcome, engine)
			require.Equal(t, 6.0, *outcome.Digest.Usage.AICredits, "both attempts and both graders must be billed")
			require.Equal(t, 6.0, *outcome.Digest.Usage.ModelMetrics["model"].AICredits)
		})
	}
}

func TestBenchmarkCacheExcludesHistoricalUsageOnReusedEngine(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: task\ninputs:\n  prompt: answer\n"), 0o600))
	calls := 0
	engine := &sessionAccountingEngine{usage: make(map[string]*models.UsageStats)}
	engine.execute = func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		calls++
		usage := sessionAccountUsage(100, 5)
		engine.usage["historical"] = usage
		return &execution.ExecutionResponse{SessionID: "historical", Success: true, FinalOutput: "answer", Usage: usage, UsageIsCumulative: true}, nil
	}
	spec := &models.EvalSpec{
		Tasks: []string{"task.yaml"}, Config: models.Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 30},
	}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithCache(cache.New(t.TempDir())))
	first, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Equal(t, 5.0, *first.Digest.Usage.AICredits)
	second, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, calls, "cached evaluation must not execute tasks")
	require.True(t, second.TestOutcomes[0].Cached)
	require.Equal(t, 100, second.TestOutcomes[0].Runs[0].SessionDigest.Usage.InputTokens)
	require.Equal(t, 5.0, *second.TestOutcomes[0].Runs[0].SessionDigest.Usage.AICredits)
	execution.UpdateOutcomeUsage(second, engine)
	require.NotNil(t, second.EvaluationUsage)
	require.Empty(t, second.EvaluationUsage.Sessions)
	require.Nil(t, second.Digest.Usage, "historical collectors must not become current billing")
}

func TestResponderClassifierExecutionsAreAccounted(t *testing.T) {
	engine := &sessionAccountingEngine{}
	taskTurns, responderTurns := 0, 0
	engine.execute = func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		if len(req.Tools) > 0 && strings.HasPrefix(req.Tools[0].Name, "responder_") {
			responderTurns++
			idx := 0
			if responderTurns > 1 {
				idx = 1 // stop
			}
			_, err := req.Tools[idx].Handler(copilot.ToolInvocation{Arguments: map[string]any{"answer": "reply"}})
			if err != nil {
				return nil, err
			}
			return &execution.ExecutionResponse{
				SessionID: "classifier", Success: true, Usage: sessionAccountUsage(10*responderTurns, float64(responderTurns)), UsageIsCumulative: true,
			}, nil
		}
		taskTurns++
		return &execution.ExecutionResponse{
			SessionID: "task", Success: true, FinalOutput: "answer", Usage: sessionAccountUsage(10*taskTurns, float64(taskTurns)), UsageIsCumulative: true,
		}, nil
	}
	spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 30}}
	runner := NewEvalRunner(config.NewEvalConfig(spec), engine, WithSkipGraders())
	ctx, scope := execution.NewUsageScope(t.Context())
	run := runner.executeRun(ctx, &models.TestCase{
		TestID: "task", Stimulus: models.TaskStimulus{Message: "initial", Responder: &models.ResponderConfig{Instructions: "reply", MaxFollowups: 3}},
	}, 1)
	require.Equal(t, models.ResponderOutcomeStopped, run.Responder.Outcome)
	require.Equal(t, 2, responderTurns)
	outcome := &models.EvaluationOutcome{EvaluationUsage: scope.Snapshot(), TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{run}}}}
	execution.UpdateOutcomeUsage(outcome, engine)
	require.Len(t, outcome.EvaluationUsage.Sessions, 2)
	require.Equal(t, 4.0, *outcome.Digest.Usage.AICredits)
	require.Equal(t, 40, outcome.Digest.Usage.InputTokens)
}
