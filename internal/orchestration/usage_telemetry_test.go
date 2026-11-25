package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/hooks"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
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

type setupFailureClient struct {
	copilotfake.Client
	calls atomic.Int32
}

func (c *setupFailureClient) CreateSession(context.Context, *copilot.CreateSessionOptions) (execution.CopilotSession, error) {
	c.calls.Add(1)
	return nil, fmt.Errorf("synthetic setup failure")
}

func TestBenchmarkUsageObservesActorStarts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		engineType string
		turnStart  bool
		setupFail  bool
		wantBucket string
	}{
		{name: "engine setup fails", engineType: "copilot-sdk", setupFail: true, wantBucket: "0"},
		{name: "observed copilot turn", engineType: "copilot-sdk", turnStart: true, wantBucket: "1"},
		{name: "copilot turn not observed", engineType: "copilot-sdk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
				[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: synthetic\n"), 0o600))
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
				Config: models.Config{EngineType: tc.engineType, ModelID: "actor-model", TrialsPerTask: 1, TimeoutSec: 30},
				Tasks:  []string{"task.yaml"}}
			var engine execution.AgentEngine
			var setupClient *setupFailureClient
			if tc.setupFail {
				setupClient = &setupFailureClient{}
				engine = execution.NewCopilotEngineBuilder("actor-model", &execution.CopilotEngineBuilderOptions{
					NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return setupClient },
				}).Build()
			} else {
				client := &copilotfake.Client{Send: func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
					var events []copilot.SessionEvent
					if tc.turnStart {
						events = append(events, copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
					}
					return append(events, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "synthetic"}}), nil
				}}
				engine = client.Engine()
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders())
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			if tc.setupFail {
				require.EqualValues(t, 1, setupClient.calls.Load(), "failure must occur inside the engine")
				require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Status)
			}
			usagetelemetry.Shutdown(5 * time.Second)
			if tc.wantBucket == "" {
				select {
				case bucket := <-events:
					t.Fatalf("ambiguous count emitted bucket %q", bucket)
				default:
				}
				return
			}
			select {
			case bucket := <-events:
				require.Equal(t, tc.wantBucket, bucket)
			default:
				t.Fatal("benchmark telemetry was not delivered")
			}
		})
	}
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
	started := make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &copilotfake.Client{Send: func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return []copilot.SessionEvent{{Data: &copilot.AssistantTurnStartData{}}}, ctx.Err()
	}}
	engine := client.Engine()
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.RunBenchmark(ctx)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first actor did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("benchmark did not stop")
	}
	require.EqualValues(t, 1, calls.Load(), "queued tasks must not invoke the actor after cancellation")
	usagetelemetry.Shutdown(5 * time.Second)
	select {
	case bucket := <-events:
		require.Equal(t, "1", bucket)
	default:
		t.Fatal("benchmark telemetry was not delivered")
	}
}

func startUsageCollector(t *testing.T, dir string) <-chan string {
	t.Helper()
	events := make(chan string, 16)
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
	return events
}

func expectUsageBucket(t *testing.T, events <-chan string, want string) {
	t.Helper()
	usagetelemetry.Shutdown(5 * time.Second)
	select {
	case got := <-events:
		require.NotEmpty(t, want, "unexpected event")
		require.Equal(t, want, got)
	default:
		require.Empty(t, want, "expected execution event")
	}
}

func TestBenchmarkUsageLaterActorTurn(t *testing.T) {
	for _, kind := range []string{"follow-up", "responder"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			events := startUsageCollector(t, dir)
			input := "  follow_up_prompts: [continue]\n"
			if kind == "responder" {
				input = "  responder:\n    instructions: reply\n    max_followups: 1\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
				[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: initial\n"+input), 0o600))
			var calls atomic.Int32
			client := &copilotfake.Client{Send: func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
				var events []copilot.SessionEvent
				if calls.Add(1)%2 == 0 {
					events = append(events, copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
				}
				return append(events, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "synthetic"}}), nil
			}}
			spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
				Config: models.Config{EngineType: "copilot-sdk", ModelID: "actor-model", TrialsPerTask: 2, MaxAttempts: 2, TimeoutSec: 30},
				Tasks:  []string{"task.yaml"},
				Graders: []models.GraderConfig{{Identifier: "text", Kind: models.GraderKindText,
					Parameters: models.TextGraderParameters{Contains: []string{"never-matches"}}}}}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), client.Engine())
			runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
				return &scriptedClassifier{decisions: []responder.Decision{{Kind: responder.DecisionReply, Answer: "continue"}}}
			}
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 2)
			for _, trial := range outcome.TestOutcomes[0].Runs {
				require.Equal(t, 2, trial.Attempts)
			}
			require.EqualValues(t, 8, calls.Load())
			expectUsageBucket(t, events, "1")
		})
	}
}

type observedResponderClassifier struct{}

func (observedResponderClassifier) Classify(ctx context.Context, _ string) (responder.Decision, error) {
	execution.NotifyActorStart(ctx)
	return responder.Decision{Kind: responder.DecisionReply, Answer: "continue"}, nil
}

func (observedResponderClassifier) Close(context.Context) error { return nil }

func TestBenchmarkUsageIgnoresResponderClassifierStarts(t *testing.T) {
	dir := t.TempDir()
	events := startUsageCollector(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: initial\n  responder:\n    instructions: reply\n    max_followups: 1\n"), 0o600))
	client := &copilotfake.Client{Send: func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: "synthetic"}}}, nil
	}}
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "copilot-sdk", ModelID: "actor-model", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:  []string{"task.yaml"}}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), client.Engine(), WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return observedResponderClassifier{} }
	_, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	expectUsageBucket(t, events, "")
}

func TestBenchmarkUsageIgnoresNativeInputResponderStarts(t *testing.T) {
	for _, actorStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("actor-start=%t", actorStart), func(t *testing.T) {
			task := "id: synthetic\nname: synthetic\ninputs:\n  prompt: start\n  responder: {instructions: reply, max_followups: 1}\n"
			cfg, dir := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
			events := startUsageCollector(t, dir)
			var actorCalls, responderCalls atomic.Int32
			client := &copilotfake.Client{Send: func(config *copilot.CreateSessionOptions, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
				for _, tool := range config.Tools.Definitions {
					if tool.Name == "responder_reply" || tool.Name == "responder_stop" {
						responderCalls.Add(1)
						name, args := "responder_stop", map[string]any{}
						if strings.Contains(options.Prompt, "Question: Your name?") {
							name, args = "responder_reply", map[string]any{"answer": "Ada"}
						}
						for _, candidate := range config.Tools.Definitions {
							if candidate.Name == name {
								if _, err := candidate.Handler(copilot.ToolInvocation{ToolName: name, Arguments: args}); err != nil {
									return nil, err
								}
							}
						}
						return []copilot.SessionEvent{{Data: &copilot.AssistantTurnStartData{}}}, nil
					}
				}
				actorCalls.Add(1)
				if config.Interaction.OnUserInputRequest == nil {
					return nil, fmt.Errorf("native user-input callback missing")
				}
				answer, err := config.Interaction.OnUserInputRequest(copilot.UserInputRequest{Question: "Your name?"},
					copilot.UserInputInvocation{SessionID: "actor"})
				if err != nil {
					return nil, err
				}
				var events []copilot.SessionEvent
				if actorStart {
					events = append(events, copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
				}
				return append(events, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "Hello " + answer.Answer}}), nil
			}}
			runner := NewEvalRunner(cfg, client.Engine(), WithSkipGraders())
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
			require.EqualValues(t, 1, actorCalls.Load())
			require.EqualValues(t, 2, responderCalls.Load())
			require.Equal(t, "Ada", *models.UserInputRecordsOf(run.UserInputRequests)[0].Answer)
			if actorStart {
				expectUsageBucket(t, events, "1")
			} else {
				expectUsageBucket(t, events, "")
			}
		})
	}
}

type fallbackActivityCounter struct{ starts atomic.Int32 }

func (c *fallbackActivityCounter) ModelTurnStarted() { c.starts.Add(1) }
func (*fallbackActivityCounter) ToolCallStarted()    {}

type fallbackObservationProbe struct{}

func (fallbackObservationProbe) Classify(ctx context.Context, _ string) (responder.Decision, error) {
	execution.NotifyActorStart(ctx)
	execution.NotifyActorDispatch(ctx)
	execution.ActivityObserverFrom(ctx).ModelTurnStarted()
	return responder.Decision{Kind: responder.DecisionReply, Answer: "synthetic"}, nil
}

func (fallbackObservationProbe) Close(context.Context) error { return nil }

func TestNativeInputFallbackPreservesActivityObservation(t *testing.T) {
	var actorStarts, actorDispatches atomic.Int32
	activity := &fallbackActivityCounter{}
	ctx := execution.WithActivityObserver(t.Context(), activity)
	ctx = execution.WithActorStartObserver(ctx, func() { actorStarts.Add(1) })
	ctx = execution.WithActorDispatchObserver(ctx, func() { actorDispatches.Add(1) })
	answer, err := responderUserInputFallback(fallbackObservationProbe{}, time.Second)(ctx, copilot.UserInputRequest{Question: "synthetic"})
	require.NoError(t, err)
	require.Equal(t, "synthetic", answer)
	require.Zero(t, actorStarts.Load())
	require.Zero(t, actorDispatches.Load())
	require.EqualValues(t, 1, activity.starts.Load())
	execution.NotifyActorStart(ctx)
	execution.NotifyActorDispatch(ctx)
	require.EqualValues(t, 1, actorStarts.Load())
	require.EqualValues(t, 1, actorDispatches.Load())
}

type forwardingMockEngine struct {
	*execution.MockEngine
	calls     atomic.Int32
	hasMarker atomic.Bool
}

func (e *forwardingMockEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls.Add(1)
	e.hasMarker.Store(ctx.Value(taskExecutionMarkerKey{}) != nil)
	return e.MockEngine.Execute(ctx, req)
}

func TestBenchmarkUsageUnsupportedEngineIsUnknown(t *testing.T) {
	dir := t.TempDir()
	events := startUsageCollector(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: synthetic\n"), 0o600))
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "mock", ModelID: "offline", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:  []string{"task.yaml"}}
	engine := &forwardingMockEngine{MockEngine: execution.NewMockEngine("offline")}
	outcome, err := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()).RunBenchmark(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, engine.calls.Load())
	require.NotEqual(t, models.StatusError, outcome.TestOutcomes[0].Status)
	expectUsageBucket(t, events, "")
}

func TestBenchmarkUsageDisabledDoesNotTrack(t *testing.T) {
	usagetelemetry.Shutdown(0)
	require.False(t, usagetelemetry.Active())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: synthetic\n"), 0o600))
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "mock", ModelID: "offline", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:  []string{"task.yaml"}}
	engine := &forwardingMockEngine{MockEngine: execution.NewMockEngine("offline")}
	_, err := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()).RunBenchmark(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, engine.calls.Load())
	require.False(t, engine.hasMarker.Load())
}

func TestBenchmarkUsageCountBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		started    int
		unknown    int
		wantBucket string
	}{
		{name: "unknown within bucket", started: 2, unknown: 2, wantBucket: "2_5"},
		{name: "unknown crosses bucket", started: 1, unknown: 1},
		{name: "unknown crosses upper edge", started: 5, unknown: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			events := startUsageCollector(t, dir)
			var tasks []string
			for i := range tc.started + tc.unknown {
				name := fmt.Sprintf("task-%d", i)
				prompt := "unknown"
				if i < tc.started {
					prompt = "observed"
				}
				tasks = append(tasks, name+".yaml")
				require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"),
					[]byte("id: "+name+"\nname: "+name+"\ninputs:\n  prompt: "+prompt+"\n"), 0o600))
			}
			client := &copilotfake.Client{Send: func(_ *copilot.CreateSessionOptions, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
				var events []copilot.SessionEvent
				if options.Prompt == "observed" {
					events = append(events, copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
				}
				return append(events, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "synthetic"}}), nil
			}}
			spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
				Config: models.Config{EngineType: "copilot-sdk", ModelID: "actor-model", TrialsPerTask: 1, TimeoutSec: 30},
				Tasks:  tasks}
			_, err := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), client.Engine(), WithSkipGraders()).RunBenchmark(t.Context())
			require.NoError(t, err)
			expectUsageBucket(t, events, tc.wantBucket)
		})
	}
}

type delayedStartClient struct {
	copilotfake.Client
	session chan *delayedStartSession
}

func (c *delayedStartClient) CreateSession(ctx context.Context, config *copilot.CreateSessionOptions) (execution.CopilotSession, error) {
	inner, err := c.Client.CreateSession(ctx, config)
	if err != nil {
		return nil, err
	}
	session := &delayedStartSession{CopilotSession: inner}
	c.session <- session
	return session, nil
}

type delayedStartSession struct {
	execution.CopilotSession
	handler copilot.SessionEventHandler
}

func (s *delayedStartSession) On(handler copilot.SessionEventHandler) func() {
	if s.handler == nil {
		s.handler = handler
	}
	return s.CopilotSession.On(handler)
}

func TestBenchmarkUsageLateCallbackCannotChangeCount(t *testing.T) {
	dir := t.TempDir()
	events := startUsageCollector(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: synthetic\n"), 0o600))
	client := &delayedStartClient{session: make(chan *delayedStartSession, 1)}
	client.Send = func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: "synthetic"}}}, nil
	}
	engine := execution.NewCopilotEngineBuilder("actor-model", &execution.CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	}).Build()
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "copilot-sdk", ModelID: "actor-model", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:  []string{"task.yaml"}}
	_, err := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()).RunBenchmark(t.Context())
	require.NoError(t, err)
	session := <-client.session
	require.NotNil(t, session.handler)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				session.handler(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
			}
		}()
	}
	wg.Wait()
	expectUsageBucket(t, events, "")
}

func TestBenchmarkUsageCallbackRacesCompletion(t *testing.T) {
	dir := t.TempDir()
	events := startUsageCollector(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: synthetic\nname: synthetic\ninputs:\n  prompt: synthetic\n"), 0o600))
	client := &delayedStartClient{session: make(chan *delayedStartSession, 1)}
	started, release := make(chan struct{}), make(chan struct{})
	client.Send = func(_ *copilot.CreateSessionOptions, _ copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		close(started)
		<-release
		return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: "synthetic"}}}, nil
	}
	engine := execution.NewCopilotEngineBuilder("actor-model", &execution.CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	}).Build()
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "synthetic"},
		Config: models.Config{EngineType: "copilot-sdk", ModelID: "actor-model", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:  []string{"task.yaml"}}
	done := make(chan error, 1)
	go func() {
		_, err := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()).RunBenchmark(t.Context())
		done <- err
	}()
	session := <-client.session
	<-started
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			for range 100 {
				session.handler(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
			}
		}()
	}
	close(release)
	require.NoError(t, <-done)
	wg.Wait()
	usagetelemetry.Shutdown(5 * time.Second)
	select {
	case bucket := <-events:
		require.Equal(t, "1", bucket)
	default:
		// A callback observed after the call completed leaves the count unknown.
	}
}
