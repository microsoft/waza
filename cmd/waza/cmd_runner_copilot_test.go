package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The fake is below the concrete production engine, not a replacement engine.
// Unexpected client/session operations panic via the embedded nil interfaces.
type runnerSDKClient struct {
	execution.CopilotClient
	t                             *testing.T
	mode                          string
	starts, creates, sends, stops atomic.Int32
	deletes                       atomic.Int32
	forced                        chan struct{}
	forceOnce                     sync.Once
	config                        *copilot.SessionConfig
	workspace                     string
}

func (c *runnerSDKClient) Start(context.Context) error {
	c.starts.Add(1)
	if c.mode == "startup-failure" {
		return errors.New("injected startup rejection")
	}
	if c.mode == "startup-hang" {
		<-c.forced
		return errors.New("transport killed during startup")
	}
	return nil
}

func (c *runnerSDKClient) Stop() error {
	c.stops.Add(1)
	if c.mode == "shutdown-hang" {
		<-c.forced
		return errors.New("transport killed during shutdown")
	}
	return nil
}

func (c *runnerSDKClient) ForceStop() { c.forceOnce.Do(func() { close(c.forced) }) }

func (c *runnerSDKClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return &copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil
}

func (c *runnerSDKClient) ListModels(context.Context) ([]copilot.ModelInfo, error) {
	model := copilot.ModelInfo{ID: "explicit-test-model", SupportedReasoningEfforts: []string{"low"}}
	model.Capabilities.Supports.ReasoningEffort = true
	return []copilot.ModelInfo{model}, nil
}

func (c *runnerSDKClient) DeleteSession(context.Context, string) error {
	c.deletes.Add(1)
	if c.mode == "delete-failure" {
		return errors.New("injected delete failure")
	}
	return nil
}

func (c *runnerSDKClient) CreateSession(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
	c.creates.Add(1)
	c.config = cfg
	require.Equal(c.t, "explicit-test-model", cfg.Model)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableSkills)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableConfigDiscovery)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableFileHooks)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableOnDemandInstructionDiscovery)
	require.Equal(c.t, copilot.Bool(true), cfg.SkipCustomInstructions)
	require.Equal(c.t, copilot.Bool(true), cfg.SkipEmbeddingRetrieval)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableSessionStore)
	require.Equal(c.t, copilot.Bool(false), cfg.EnableHostGitOperations)
	require.Equal(c.t, &copilot.MemoryConfiguration{Enabled: false}, cfg.Memory)
	require.Empty(c.t, cfg.HookDirectories)
	require.Empty(c.t, cfg.PluginDirectories)
	require.Empty(c.t, cfg.InstructionDirectories)
	require.Empty(c.t, cfg.SkillDirectories)
	require.Empty(c.t, cfg.MCPServers)
	require.Empty(c.t, cfg.Tools)
	require.NotNil(c.t, cfg.AvailableTools)
	require.Empty(c.t, cfg.AvailableTools, "no model-initiated tools may be exposed")
	require.NotNil(c.t, cfg.OnPermissionRequest)
	require.NotNil(c.t, cfg.Hooks.OnPreToolUse)
	c.workspace = cfg.WorkingDirectory
	require.NotEmpty(c.t, c.workspace)
	if c.mode == "create-hang" {
		<-c.forced
		return nil, errors.New("transport killed during session creation/options update")
	}
	return &runnerSDKSession{client: c}, nil
}

type runnerSDKSession struct {
	execution.CopilotSession
	client   *runnerSDKClient
	handlers []copilot.SessionEventHandler
}

func (s *runnerSDKSession) SessionID() string { return "fake-selected-session" }
func (s *runnerSDKSession) Disconnect() error {
	if s.client.mode == "disconnect-failure" {
		return errors.New("injected disconnect failure")
	}
	return nil
}
func (s *runnerSDKSession) On(handler copilot.SessionEventHandler) func() {
	s.handlers = append(s.handlers, handler)
	return func() {}
}
func (s *runnerSDKSession) SendAndWait(_ context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error) {
	c := s.client
	c.sends.Add(1)
	require.Equal(c.t, "hello", options.Prompt)
	if c.mode == "send-hang" {
		// Deliberately ignore the RPC context: only killing the private
		// transport can unblock this request.
		<-c.forced
		return nil, errors.New("transport killed during send")
	}
	if c.mode == "denied-tool" {
		decision, err := c.config.OnPermissionRequest(&copilot.PermissionRequestShell{}, copilot.PermissionInvocation{})
		require.NoError(c.t, err)
		require.IsType(c.t, &rpc.PermissionDecisionReject{}, decision)
		out, err := c.config.Hooks.OnPreToolUse(copilot.PreToolUseHookInput{ToolName: "bash"}, copilot.HookInvocation{})
		require.NoError(c.t, err)
		require.Equal(c.t, "deny", out.PermissionDecision)
	}
	for _, handler := range s.handlers {
		handler(copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "hello", Model: new("explicit-test-model")}})
	}
	return &copilot.SessionEvent{}, nil
}
func (s *runnerSDKSession) UsageMetrics(context.Context) (*rpc.UsageGetMetricsResult, error) {
	return &rpc.UsageGetMetricsResult{
		TotalNanoAiu: new(0.0),
		ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
			"explicit-test-model": {TotalNanoAiu: new(0.0), Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 7}},
		},
	}, nil
}

func copilotRunnerRequest(t *testing.T) runnercontract.Request {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/copilot-unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	request.Input.Eval = strings.ReplaceAll(request.Input.Eval, "gpt-4.1", "explicit-test-model")
	return *request
}

func runnerSDKService(t *testing.T, mode string) (*runnerunit.Service, *[]*runnerSDKClient) {
	t.Helper()
	var clients []*runnerSDKClient
	service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		client := &runnerSDKClient{t: t, mode: mode, forced: make(chan struct{})}
		clients = append(clients, client)
		return execution.NewCopilotEngineBuilder(cfg.ModelID, &execution.CopilotEngineBuilderOptions{
			NewCopilotClient:        func(*copilot.ClientOptions) execution.CopilotClient { return client },
			DisableCommandMocks:     true,
			DisableAmbientDiscovery: true,
			DiagnosticObserver:      func(execution.ExecutionDiagnostic) error { return nil },
		}).Build(), nil
	})
	return service, &clients
}

func runCopilotCLI(t *testing.T, ctx context.Context, service *runnerunit.Service, request runnercontract.Request) runnerterminal.Envelope {
	t.Helper()
	data, err := json.Marshal(request)
	require.NoError(t, err)
	cmd := newRunnerCommandWithService(service)
	cmd.SetArgs([]string{"run"})
	cmd.SetIn(bytes.NewReader(data))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	runErr := cmd.ExecuteContext(ctx)
	envelope := validateRunnerEnvelope(t, output.Bytes(), request)
	if envelope.ExitCode == 0 {
		require.NoError(t, runErr)
	} else {
		exit, ok := errors.AsType[*ExitCodeError](runErr)
		require.True(t, ok, "%v", runErr)
		require.Equal(t, envelope.ExitCode, exit.Code)
	}
	return envelope
}

func validateRunnerEnvelope(t *testing.T, raw []byte, request runnercontract.Request) runnerterminal.Envelope {
	t.Helper()
	var envelope runnerterminal.Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	require.NoError(t, decoder.Decode(&envelope))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	var binding *runnerterminal.Binding
	if envelope.Identity != nil {
		id, err := runneridentity.Derive(&request)
		require.NoError(t, err)
		mode := "mock"
		for _, capability := range request.RequiredCapabilities {
			if capability == "inline.copilot-text" {
				mode = "copilot"
			}
		}
		binding = &runnerterminal.Binding{
			Selection: request.Selection, Identity: id, GraderIDs: []string{"_output_contains"}, ExecutionMode: mode,
		}
	}
	_, err := runnerterminal.Validate(raw, binding)
	require.NoError(t, err)
	return envelope
}

func runRunnerHTTP(t *testing.T, server *httptest.Server, request runnercontract.Request) runnerterminal.Envelope {
	t.Helper()
	data, err := json.Marshal(request)
	require.NoError(t, err)
	response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	envelope := validateRunnerEnvelope(t, raw, request)
	require.Equal(t, strconv.Itoa(envelope.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
	return envelope
}

func TestRunnerCopilotExactCLIHTTP(t *testing.T) {
	for _, transport := range []string{"cli", "http"} {
		t.Run(transport, func(t *testing.T) {
			service, clients := runnerSDKService(t, "")
			mux := http.NewServeMux()
			// Share the actual public service boundary, including terminal emission.
			webapi.RegisterRunnerRoutesWithService(mux, service)
			server := httptest.NewServer(mux)
			defer server.Close()
			for _, control := range []string{"selected", "neighbor", "repeat", "last", "failed-check", "reasoning-effort", "first-event-disabled"} {
				request := copilotRunnerRequest(t)
				switch control {
				case "neighbor":
					request.Selection.Task = "neighbor"
				case "last":
					request.Selection.Trial, request.Selection.Attempt = 3, 3
				case "failed-check":
					request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- absent-marker", 1)
				case "reasoning-effort":
					request.Input.Eval = strings.Replace(request.Input.Eval, "  executor:", "  reasoning_effort: low\n  executor:", 1)
				case "first-event-disabled":
					request.Input.Eval = strings.Replace(request.Input.Eval, "  executor:", "  first_event_timeout_seconds: 1\n  executor:", 1)
					request.Input.Task += "first_event_timeout_seconds: 0\n"
				}
				before := len(*clients)
				var envelope runnerterminal.Envelope
				if transport == "cli" {
					envelope = runCopilotCLI(t, t.Context(), service, request)
				} else {
					envelope = runRunnerHTTP(t, server, request)
				}
				if control == "neighbor" {
					require.Equal(t, before, len(*clients))
					require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
					continue
				}
				require.Len(t, *clients, before+1)
				c := (*clients)[before]
				if control == "reasoning-effort" {
					require.Equal(t, "low", c.config.ReasoningEffort)
				}
				require.EqualValues(t, 1, c.starts.Load())
				require.EqualValues(t, 1, c.creates.Load())
				require.EqualValues(t, 1, c.sends.Load())
				require.EqualValues(t, 1, c.deletes.Load())
				require.EqualValues(t, 1, c.stops.Load())
				require.Equal(t, request.Selection, *envelope.Selection)
				require.Equal(t, request.Selection.Trial, envelope.Observation.RunNumber)
				require.Equal(t, request.Selection.Attempt, envelope.Observation.Attempts)
				require.Equal(t, "copilot", envelope.Observation.ExecutionMode)
				require.Equal(t, 1, envelope.EngineCalls)
				want := runnerterminal.Success
				if control == "failed-check" {
					want = runnerterminal.EvaluationFailure
				}
				require.Equal(t, want, envelope.Outcome, "%+v", envelope.Diagnostics)
				_, err := os.Stat(c.workspace)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestRunnerMetricsCLIHTTP(t *testing.T) {
	for _, profile := range []string{"mock", "copilot"} {
		for _, transport := range []string{"cli", "http"} {
			t.Run(profile+"/"+transport, func(t *testing.T) {
				base := copilotRunnerRequest(t)
				if profile == "mock" {
					base.RequiredCapabilities = []string{"exact-unit", "inline.mock-text"}
					base.Input.Eval = strings.Replace(base.Input.Eval, "executor: copilot-sdk", "executor: mock", 1)
					base.AllowPaidExecution = false
				}
				prefix, rest, ok := strings.Cut(base.Input.Eval, "metrics:\n")
				require.True(t, ok)
				_, suffix, ok := strings.Cut(rest, "tasks:")
				require.True(t, ok)
				var constructed int
				service, clients := runnerSDKService(t, "")
				if profile == "mock" {
					service = runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
						constructed++
						return execution.NewMockEngine(cfg.ModelID), nil
					})
				}
				mux := http.NewServeMux()
				webapi.RegisterRunnerRoutesWithService(mux, service)
				server := httptest.NewServer(mux)
				defer server.Close()
				var previousIdentity string
				for _, control := range []struct {
					name, declaration string
					valid             bool
				}{
					{"omitted", "", true},
					{"empty", "metrics: []", true},
					{"canonical", "metrics: [{name: quality, weight: 1, threshold: 0.8}]", true},
					{"changed-threshold", "metrics: [{name: quality, weight: 1, threshold: 0.9}]", true},
					{"zero-threshold", "metrics: [{name: quality, weight: 1, threshold: 0}]", true},
					{"one-threshold", "metrics: [{name: quality, weight: 1, threshold: 1, enabled: true}]", true},
					{"disabled", "metrics: [{name: quality, weight: 0.5, threshold: 0.8, enabled: false}]", true},
					{"small-weight", "metrics: [{name: quality, weight: 1e-300, threshold: 0.8}]", true},
					{"null", "metrics: null", profile == "mock"},
					{"mapping", "metrics: {name: quality, weight: 1, threshold: 1}", false},
					{"null-entry", "metrics: [null]", false},
					{"missing-name", "metrics: [{weight: 1, threshold: 1}]", false},
					{"empty-name", "metrics: [{name: '', weight: 1, threshold: 1}]", false},
					{"missing-weight", "metrics: [{name: quality, threshold: 1}]", false},
					{"zero-weight", "metrics: [{name: quality, weight: 0, threshold: 1}]", false},
					{"negative-weight", "metrics: [{name: quality, weight: -1, threshold: 1}]", false},
					{"string-weight", "metrics: [{name: quality, weight: '1', threshold: 1}]", false},
					{"null-weight", "metrics: [{name: quality, weight: null, threshold: 1}]", false},
					{"missing-threshold", "metrics: [{name: quality, weight: 1}]", false},
					{"null-threshold", "metrics: [{name: quality, weight: 1, threshold: null}]", false},
					{"negative-threshold", "metrics: [{name: quality, weight: 1, threshold: -0.00000000000000000001}]", false},
					{"above-one", "metrics: [{name: quality, weight: 1, threshold: 1.00000000000000000001}]", false},
					{"null-enabled", "metrics: [{name: quality, weight: 1, threshold: 1, enabled: null}]", false},
					{"string-enabled", "metrics: [{name: quality, weight: 1, threshold: 1, enabled: 'false'}]", false},
					{"unknown-field", "metrics: [{name: quality, weight: 1, threshold: 1, extra: true}]", false},
					{"duplicate-field", "metrics: [{name: quality, weight: 1, threshold: 0, threshold: 1}]", false},
					{"alias", "metrics: [&metric {name: quality, weight: 1, threshold: 1}, *metric]", false},
					{"nan", "metrics: [{name: quality, weight: .nan, threshold: 1}]", false},
					{"infinite", "metrics: [{name: quality, weight: .inf, threshold: 1}]", false},
					{"underflow", "metrics: [{name: quality, weight: 1e-999999999, threshold: 1}]", false},
				} {
					t.Run(control.name, func(t *testing.T) {
						request := base
						request.Input.Eval = prefix + control.declaration + "\ntasks:" + suffix
						before := constructed + len(*clients)
						var envelope runnerterminal.Envelope
						if transport == "cli" {
							envelope = runCopilotCLI(t, t.Context(), service, request)
						} else {
							envelope = runRunnerHTTP(t, server, request)
						}
						if !control.valid {
							require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome, "%+v", envelope.Diagnostics)
							require.Zero(t, envelope.EngineCalls)
							require.Nil(t, envelope.Identity)
							require.Equal(t, before, constructed+len(*clients))
							return
						}
						require.Equal(t, runnerterminal.Success, envelope.Outcome, "%+v", envelope.Diagnostics)
						require.Equal(t, before+1, constructed+len(*clients))
						require.Equal(t, 1, envelope.EngineCalls)
						require.Equal(t, profile, envelope.Observation.ExecutionMode)
						require.NotEmpty(t, envelope.Observation.Validations)
						identity, err := runneridentity.Derive(&request)
						require.NoError(t, err)
						require.Equal(t, identity, envelope.Identity)
						require.NotEqual(t, previousIdentity, identity.CanonicalDigest, "changed metric metadata must remain identity-bound")
						previousIdentity = identity.CanonicalDigest
					})
				}
			})
		}
	}
}

func TestRunnerCopilotAdmissionNeverConstructsRuntime(t *testing.T) {
	for _, control := range []string{"no-payment", "missing-profile", "optional-profile", "conflict", "wrong-engine", "noncanonical-engine", "empty-model", "skills", "prompt-file", "out-of-range", "unsupported-version", "first-event-eval", "first-event-task"} {
		t.Run(control, func(t *testing.T) {
			request := copilotRunnerRequest(t)
			switch control {
			case "no-payment":
				request.AllowPaidExecution = false
			case "missing-profile":
				request.RequiredCapabilities = []string{"exact-unit"}
			case "optional-profile":
				request.RequiredCapabilities = []string{"exact-unit"}
				request.OptionalCapabilities = []string{"inline.copilot-text"}
			case "conflict":
				request.RequiredCapabilities = append(request.RequiredCapabilities, "inline.mock-text")
			case "wrong-engine":
				request.Input.Eval = strings.Replace(request.Input.Eval, "executor: copilot-sdk", "executor: mock", 1)
			case "noncanonical-engine":
				request.Input.Eval = strings.Replace(request.Input.Eval, "executor: copilot-sdk", "executor: copilot", 1)
			case "empty-model":
				request.Input.Eval = strings.Replace(request.Input.Eval, "model: explicit-test-model", `model: ""`, 1)
			case "skills":
				request.Input.Task += "skill_directories: [ambient]\n"
			case "prompt-file":
				request.Input.Task = strings.Replace(request.Input.Task, "prompt: hello", "prompt_file: secret.txt", 1)
			case "out-of-range":
				request.Selection.Attempt = 4
			case "unsupported-version":
				request.Versions = []string{"99.0"}
			case "first-event-eval":
				request.Input.Eval = strings.Replace(request.Input.Eval, "  executor:", "  first_event_timeout_seconds: 1\n  executor:", 1)
			case "first-event-task":
				request.Input.Task += "first_event_timeout_seconds: 1\n"
			}
			service, clients := runnerSDKService(t, "send-hang")
			envelope := runCopilotCLI(t, t.Context(), service, request)
			require.Contains(t, []runnerterminal.Outcome{runnerterminal.InvalidInput, runnerterminal.UnsupportedCapability}, envelope.Outcome)
			require.Empty(t, *clients)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Identity)
		})
	}
}

func TestRunnerCopilotMissingRuntimePreservesCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-copilot")
	t.Setenv("COPILOT_CLI_PATH", path)
	envelope := runCopilotCLI(t, t.Context(), runnerunit.NewService(nil), copilotRunnerRequest(t))
	require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
	require.Zero(t, envelope.EngineCalls)
	require.Nil(t, envelope.Observation)
	require.Contains(t, envelope.Diagnostics[0].Message, path)
	require.NotContains(t, envelope.Diagnostics[0].Message, "force-stoppable")
}

func TestRunnerCopilotExampleSchemaParity(t *testing.T) {
	request := copilotRunnerRequest(t)
	for _, fixture := range []struct {
		name, source, schema string
	}{
		{"eval", request.Input.Eval, schemas.EvalSchemaJSON},
		{"task", request.Input.Task, schemas.TaskSchemaJSON},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var schemaDocument any
			require.NoError(t, json.Unmarshal([]byte(fixture.schema), &schemaDocument))
			compiler := jsonschema.NewCompiler()
			compiler.UseLoader(schemaloader.Offline{})
			require.NoError(t, compiler.AddResource("fixture.schema.json", schemaDocument))
			schema, err := compiler.Compile("fixture.schema.json")
			require.NoError(t, err)
			var document any
			require.NoError(t, yaml.Unmarshal([]byte(fixture.source), &document))
			data, err := json.Marshal(document)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &document))
			require.NoError(t, schema.Validate(document))
			if fixture.name == "eval" {
				root, ok := document.(map[string]any)
				require.True(t, ok)
				config, ok := root["config"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "copilot-sdk", config["executor"])
				config["executor"] = "copilot"
				require.Error(t, schema.Validate(document), "terminal mode is not a valid eval executor")
			}
		})
	}
}

func TestRunnerCopilotBoundedLifecycle(t *testing.T) {
	// Signal registration belongs outside virtual-time bubbles; keep the OS
	// handler active while the real Cobra command registers its own listener.
	_, stopSignals := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	for _, control := range []string{"startup-failure", "startup-hang", "create-hang", "send-hang", "task-timeout", "cancel", "shutdown-hang", "denied-tool", "delete-failure", "disconnect-failure"} {
		t.Run(control, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				request := copilotRunnerRequest(t)
				request.TimeoutMS = 1000
				mode := control
				if control == "cancel" || control == "task-timeout" {
					mode = "send-hang"
				}
				if control == "task-timeout" {
					request.TimeoutMS = 60000
					request.Input.Task += "timeout_seconds: 1\n"
				}
				if control == "shutdown-hang" {
					request.TimeoutMS = 60000
				}
				service, clients := runnerSDKService(t, mode)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if control == "cancel" {
					go func() {
						time.Sleep(100 * time.Millisecond)
						cancel()
					}()
				}
				started := time.Now()
				envelope := runCopilotCLI(t, ctx, service, request)
				require.Len(t, *clients, 1)
				c := (*clients)[0]
				switch control {
				case "startup-hang", "create-hang", "send-hang", "task-timeout", "shutdown-hang":
					require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
					require.LessOrEqual(t, time.Since(started), 6*time.Second)
					if control == "shutdown-hang" {
						require.Equal(t, 5*time.Second, time.Since(started), "separate shutdown budget, not the longer request budget")
					}
				case "cancel":
					require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
					require.LessOrEqual(t, time.Since(started), 6*time.Second)
				default:
					require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
				}
				if control == "startup-failure" {
					require.Zero(t, envelope.EngineCalls)
					require.Nil(t, envelope.Observation)
					require.Contains(t, envelope.Diagnostics[0].Message, "initialize/start_failed")
					require.Zero(t, time.Since(started), "rejection is not an expired deadline")
				}
				if control == "create-hang" {
					require.EqualValues(t, 1, c.creates.Load())
					require.Zero(t, c.sends.Load())
				}

				require.LessOrEqual(t, c.sends.Load(), int32(1))
				require.NotZero(t, c.stops.Load())
				require.NotNil(t, envelope.Identity, "admitted lineage survives failures")
				if c.workspace != "" {
					_, err := os.Stat(c.workspace)
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			})
		})
	}
}

func TestRunnerCopilotRejectsMismatchedRuntimeAndLocalProjection(t *testing.T) {
	request := copilotRunnerRequest(t)
	for _, control := range []string{"mock", "wrapper", "nil", "typed-nil"} {
		t.Run(control, func(t *testing.T) {
			wrapper := &publicUnitEngine{}
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				switch control {
				case "mock":
					return execution.NewMockEngine("mock"), nil
				case "wrapper":
					return wrapper, nil
				case "typed-nil":
					return (*execution.CopilotEngine)(nil), nil
				default:
					return nil, nil
				}
			})
			envelope := runCopilotCLI(t, t.Context(), service, request)
			require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Observation)
			require.NotNil(t, envelope.Identity)
			require.Zero(t, wrapper.initializes.Load())
			require.Zero(t, wrapper.calls.Load())
			require.Zero(t, wrapper.shutdowns.Load())
		})
	}
	service, clients := runnerSDKService(t, "")
	data, err := json.Marshal(request)
	require.NoError(t, err)
	outcome, err := service.RunLocal(t.Context(), data)
	require.ErrorContains(t, err, "supports only inline.mock-text")
	require.Nil(t, outcome)
	require.Empty(t, *clients)
}
