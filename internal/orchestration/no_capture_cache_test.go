package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type captureCacheEngine struct {
	*execution.MockEngine
	large    bool
	requests []*execution.ExecutionRequest
}

func (e *captureCacheEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.requests = append(e.requests, req)
	copyReq := *req
	if e.large {
		copyReq.Resources = append([]execution.ResourceFile(nil), req.Resources...)
		copyReq.Resources = append(copyReq.Resources, execution.ResourceFile{Path: "unrelated.bin", Content: []byte(strings.Repeat("x", 16*1024*1024+1))})
	}
	return e.MockEngine.Execute(ctx, &copyReq)
}

func TestWorkspaceCaptureCacheTransitions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first       string
		second      string
		large       bool
		wantStatus  models.Status
		wantCapture bool
	}{
		{"none_to_full_rechecks_limit", "none", "full", true, models.StatusError, false},
		{"full_to_none_executes", "full", "none", false, models.StatusPassed, true},
		{"omitted_to_none_executes", "", "none", false, models.StatusPassed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &models.EvalSpec{Config: models.Config{ModelID: "mock", TimeoutSec: 30, TrialsPerTask: 1, WorkspaceCapture: tc.first}}
			task := &models.TestCase{TestID: "test", DisplayName: "test", Stimulus: models.TaskStimulus{Message: "hello"}}
			engine := &captureCacheEngine{MockEngine: execution.NewMockEngine("mock"), large: tc.large}
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine, WithCache(cache.New(t.TempDir())))
			first, cached := runner.runTest(t.Context(), task, 1, 1)
			require.False(t, cached)
			require.Equal(t, models.StatusPassed, first.Status, first.Runs)
			spec.Config.WorkspaceCapture = tc.second
			second, cached := runner.runTest(t.Context(), task, 1, 1)
			require.False(t, cached)
			require.Equal(t, tc.wantStatus, second.Status, second.Runs)
			require.Len(t, engine.requests, 2)
			require.Equal(t, tc.wantCapture, engine.requests[1].SkipWorkspaceCapture)
			if tc.large {
				require.Contains(t, second.Runs[0].ErrorMsg, "workspace capture incomplete")
			}
		})
	}
}
