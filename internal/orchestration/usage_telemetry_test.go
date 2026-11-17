package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/hooks"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
)

func TestBenchmarkUsageCountsExecutedTasks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"first", "second"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"),
			[]byte("id: "+name+"\nname: "+name+"\ninputs:\n  prompt: synthetic\n"), 0o600))
	}

	var mu sync.Mutex
	var buckets []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event struct {
			Count string `json:"task_count_bucket"`
		}
		if json.NewDecoder(r.Body).Decode(&event) == nil {
			mu.Lock()
			buckets = append(buckets, event.Count)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(server.URL, server.Client(), true, false))
	path := filepath.Join(dir, "telemetry.json")
	p := usagetelemetry.Resolve(usagetelemetry.Inputs{Getenv: func(string) string { return "" }, SettingsPath: path, WorkDir: dir})
	require.True(t, p.Enabled)
	usagetelemetry.Start(p, "dev")
	t.Cleanup(func() { usagetelemetry.Shutdown(0) })
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "mock", ModelID: "offline", TrialsPerTask: 3,
			MaxAttempts: 2, TimeoutSec: 5},
		Tasks: []string{"first.yaml", "second.yaml"},
	}
	resultCache := cache.New(filepath.Join(dir, "cache"))
	run := func() {
		t.Helper()
		runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), execution.NewMockEngine("offline"), WithCache(resultCache))
		_, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
	}
	run()
	run()
	spec.Hooks.BeforeTask = []hooks.HookConfig{{Command: hookCommand(t, "fail-first", filepath.Join(dir, "marker")), ErrorOnFail: true}}
	run()
	spec.Hooks = hooks.HooksConfig{}
	spec.Tasks = []string{"first.yaml"}
	spec.Graders = []models.GraderConfig{{Identifier: "text", Kind: models.GraderKindText,
		Parameters: models.TextGraderParameters{Contains: []string{"synthetic-never-matches"}}}}
	retryRunner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), execution.NewMockEngine("offline"))
	outcome, err := retryRunner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes[0].Runs, 3)
	for _, trial := range outcome.TestOutcomes[0].Runs {
		require.Equal(t, 2, trial.Attempts)
	}
	usagetelemetry.Shutdown(5 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"2_5", "0", "1", "1"}, buckets)
}

type cancelQueuedEngine struct {
	execution.AgentEngine
	started chan struct{}
	calls   atomic.Int32
}

func (e *cancelQueuedEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBenchmarkUsageExcludesCanceledQueuedTasks(t *testing.T) {
	dir := t.TempDir()
	var tasks []string
	for i := range 8 {
		name := fmt.Sprintf("task-%d", i)
		tasks = append(tasks, name+".yaml")
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"),
			[]byte("id: "+name+"\nname: "+name+"\ninputs:\n  prompt: synthetic\n"), 0o600))
	}
	events := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event struct {
			Count string `json:"task_count_bucket"`
		}
		if json.NewDecoder(r.Body).Decode(&event) == nil {
			events <- event.Count
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(server.URL, server.Client(), true, false))
	p := usagetelemetry.Resolve(usagetelemetry.Inputs{Getenv: func(string) string { return "" },
		SettingsPath: filepath.Join(dir, "telemetry.json"), WorkDir: dir})
	require.True(t, p.Enabled)
	usagetelemetry.Start(p, "dev")
	t.Cleanup(func() { usagetelemetry.Shutdown(0) })
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "mock", ModelID: "offline", TrialsPerTask: 1, TimeoutSec: 30,
			Concurrent: true, Workers: 1}, Tasks: tasks}
	engine := &cancelQueuedEngine{AgentEngine: execution.NewMockEngine("offline"), started: make(chan struct{})}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.RunBenchmark(ctx)
	}()
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first actor did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("benchmark did not stop")
	}
	require.EqualValues(t, 1, engine.calls.Load(), "queued tasks must not invoke the actor after cancellation")
	usagetelemetry.Shutdown(5 * time.Second)
	select {
	case bucket := <-events:
		require.Equal(t, "1", bucket)
	default:
		t.Fatal("benchmark telemetry was not delivered")
	}
}
