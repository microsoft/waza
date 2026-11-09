package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
	"github.com/stretchr/testify/require"
)

func TestBenchmarkOperationalErrorTrialPrecedence(t *testing.T) {
	for _, control := range []struct {
		name     string
		statuses []models.Status
		want     models.Status
	}{
		{"all error", []models.Status{models.StatusError, models.StatusError}, models.StatusError},
		{"all pass", []models.Status{models.StatusPassed, models.StatusPassed}, models.StatusPassed},
		{"all fail", []models.Status{models.StatusFailed, models.StatusFailed}, models.StatusFailed},
		{"error then pass", []models.Status{models.StatusError, models.StatusPassed}, models.StatusError},
		{"pass then error", []models.Status{models.StatusPassed, models.StatusError}, models.StatusError},
		{"error then fail", []models.Status{models.StatusError, models.StatusFailed}, models.StatusError},
		{"fail then error", []models.Status{models.StatusFailed, models.StatusError}, models.StatusError},
		{"pass then fail", []models.Status{models.StatusPassed, models.StatusFailed}, models.StatusFailed},
		{"fail then pass", []models.Status{models.StatusFailed, models.StatusPassed}, models.StatusFailed},
	} {
		t.Run(control.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
				[]byte("id: task\nname: Task\ninputs: {prompt: retained output}\n"), 0600))
			spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "classification"},
				Config: models.Config{EngineType: "copilot-sdk", ModelID: "mock-model", TimeoutSec: 30, TrialsPerTask: len(control.statuses)},
				Tasks:  []string{"task.yaml"}, Graders: []models.GraderConfig{evidenceConfig("judge")}}
			client := &copilotfake.Client{Send: func(_ *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
				return []copilot.SessionEvent{
					{Data: &copilot.UserMessageData{Content: options.Prompt}},
					{Data: &copilot.AssistantMessageData{Content: "retained output"}},
				}, nil
			}}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), client.Engine())
			calls := 0
			runner.evidenceExecutor = scriptedEvidenceJudge(func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				status := control.statuses[calls]
				calls++
				if status == models.StatusError {
					return nil, errors.New("operational judge unavailable")
				}
				tool := req.Tools[0]
				if status == models.StatusFailed {
					tool = req.Tools[1]
				}
				_, err := tool.Handler(copilot.ToolInvocation{ToolName: tool.Name})
				return &execution.ExecutionResponse{Success: true, SessionID: fmt.Sprintf("judge-%d", calls)}, err
			})
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.Equal(t, len(control.statuses), calls)
			require.Len(t, outcome.TestOutcomes, 1)
			task := outcome.TestOutcomes[0]
			require.Equal(t, control.want, task.Status)
			require.Len(t, task.Runs, len(control.statuses))
			for i, run := range task.Runs {
				require.Equal(t, control.statuses[i], run.Status)
				require.Equal(t, 1, run.Attempts)
			}
			require.Equal(t, 1, outcome.Digest.TotalTests)
			require.Equal(t, 1, outcome.Digest.Errors+outcome.Digest.Failed+outcome.Digest.Succeeded)
			switch control.want {
			case models.StatusError:
				require.Equal(t, 1, outcome.Digest.Errors)
			case models.StatusFailed:
				require.Equal(t, 1, outcome.Digest.Failed)
			case models.StatusPassed:
				require.Equal(t, 1, outcome.Digest.Succeeded)
			}
		})
	}
}

type cacheClassificationEngine struct {
	*execution.MockEngine
	calls int
	fail  bool
}

func (e *cacheClassificationEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	if e.fail {
		return nil, errors.New("operational execution unavailable")
	}
	return e.MockEngine.Execute(ctx, req)
}

func TestOperationalErrorsAreNotReusableCacheResults(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical=%t", historical), func(t *testing.T) {
			dir := t.TempDir()
			spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", ModelID: "mock-model", TimeoutSec: 30, TrialsPerTask: 1}}
			tc := &models.TestCase{TestID: "task", DisplayName: "Task", Stimulus: models.TaskStimulus{Message: "ready"}}
			cfg := config.NewEvalConfig(spec, config.WithSpecDir(dir), config.WithFixtureDir(dir))
			key, err := cache.CacheKeyWithSpecDir(spec, tc, dir, dir)
			require.NoError(t, err)
			resultCache := cache.New(t.TempDir())
			if historical {
				require.NoError(t, resultCache.Put(key, &models.TestOutcome{TestID: tc.TestID, Status: models.StatusFailed,
					Runs: []models.RunResult{{RunNumber: 1, Status: models.StatusError, ErrorMsg: "old operational error"}}}))
			}
			engine := &cacheClassificationEngine{MockEngine: execution.NewMockEngine("mock-model"), fail: !historical}
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
			runner := NewEvalRunner(cfg, engine, WithCache(resultCache))
			first, cached := runner.runTest(t.Context(), tc, 1, 1)
			require.False(t, cached)
			require.Equal(t, 1, engine.calls)
			second, cached := runner.runTest(t.Context(), tc, 1, 1)
			if historical {
				require.Equal(t, models.StatusPassed, first.Status)
				require.Equal(t, models.StatusPassed, second.Status)
				require.True(t, cached)
				require.Equal(t, 1, engine.calls)
			} else {
				require.Equal(t, models.StatusError, first.Status)
				require.Equal(t, models.StatusError, second.Status)
				require.False(t, cached)
				require.Equal(t, 2, engine.calls)
				_, found := resultCache.Get(key)
				require.False(t, found)
			}
		})
	}
}
