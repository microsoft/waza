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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
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
	require.Empty(c.t, cfg.SkillDirectories)
	require.Empty(c.t, cfg.MCPServers)
	require.Empty(c.t, cfg.Tools)
	require.NotNil(c.t, cfg.AvailableTools)
	require.Empty(c.t, cfg.AvailableTools, "no model-initiated tools may be exposed")
	require.NotNil(c.t, cfg.OnPermissionRequest)
	require.NotNil(c.t, cfg.Hooks.OnPreToolUse)
	c.workspace = cfg.WorkingDirectory
	require.NotEmpty(c.t, c.workspace)
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
			NewCopilotClient:    func(*copilot.ClientOptions) execution.CopilotClient { return client },
			DisableCommandMocks: true,
			DiagnosticObserver:  func(execution.ExecutionDiagnostic) error { return nil },
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
	raw := bytes.Clone(output.Bytes())
	var envelope runnerterminal.Envelope
	decoder := json.NewDecoder(&output)
	require.NoError(t, decoder.Decode(&envelope))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	if envelope.ExitCode == 0 {
		require.NoError(t, runErr)
	} else {
		exit, ok := errors.AsType[*ExitCodeError](runErr)
		require.True(t, ok, "%v", runErr)
		require.Equal(t, envelope.ExitCode, exit.Code)
	}
	var binding *runnerterminal.Binding
	if envelope.Identity != nil {
		id, err := runneridentity.Derive(&request)
		require.NoError(t, err)
		binding = &runnerterminal.Binding{Selection: request.Selection, Identity: id, GraderIDs: []string{"_output_contains"}}
	}
	_, err = runnerterminal.Validate(raw, binding)
	require.NoError(t, err)
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
			for _, control := range []string{"selected", "neighbor", "repeat", "last", "failed-check"} {
				request := copilotRunnerRequest(t)
				switch control {
				case "neighbor":
					request.Selection.Task = "neighbor"
				case "last":
					request.Selection.Trial, request.Selection.Attempt = 3, 3
				case "failed-check":
					request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- absent-marker", 1)
				}
				before := len(*clients)
				var envelope runnerterminal.Envelope
				if transport == "cli" {
					envelope = runCopilotCLI(t, t.Context(), service, request)
				} else {
					data, err := json.Marshal(request)
					require.NoError(t, err)
					response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(data))
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, response.StatusCode)
					decoder := json.NewDecoder(response.Body)
					require.NoError(t, decoder.Decode(&envelope))
					require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
					require.Equal(t, strconv.Itoa(envelope.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
					require.NoError(t, response.Body.Close())
				}
				if control == "neighbor" {
					require.Equal(t, before, len(*clients))
					require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
					continue
				}
				require.Len(t, *clients, before+1)
				c := (*clients)[before]
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

func TestRunnerCopilotAdmissionNeverConstructsRuntime(t *testing.T) {
	for _, control := range []string{"no-payment", "missing-profile", "optional-profile", "conflict", "wrong-engine", "empty-model", "skills", "prompt-file", "out-of-range", "unsupported-version"} {
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
				request.Input.Eval = strings.Replace(request.Input.Eval, "executor: copilot", "executor: mock", 1)
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
			}
			service, clients := runnerSDKService(t, "")
			envelope := runCopilotCLI(t, t.Context(), service, request)
			require.Contains(t, []runnerterminal.Outcome{runnerterminal.InvalidInput, runnerterminal.UnsupportedCapability}, envelope.Outcome)
			require.Empty(t, *clients)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Identity)
		})
	}
}

func TestRunnerCopilotBoundedLifecycle(t *testing.T) {
	for _, control := range []string{"startup-hang", "send-hang", "task-timeout", "cancel", "shutdown-hang", "denied-tool", "delete-failure", "disconnect-failure"} {
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
				case "startup-hang", "send-hang", "task-timeout", "shutdown-hang":
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
