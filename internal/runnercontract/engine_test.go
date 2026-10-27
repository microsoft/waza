package runnercontract

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunnerNativeFactoryDoesNotReuseAnotherRequestsClient(t *testing.T) {
	for _, name := range []string{"first-missing-cli", "second-missing-cli"} {
		path := filepath.Join(t.TempDir(), name)
		t.Setenv("COPILOT_CLI_PATH", path)
		engine, err := defaultEngine(models.Config{EngineType: "copilot-sdk", ModelID: "fixed"})
		require.NoError(t, err)
		require.ErrorContains(t, engine.Initialize(t.Context()), path)
		require.NoError(t, engine.Shutdown(t.Context()))
	}
}

type instrumentedEngine struct {
	mode                             string
	initializes, executes, shutdowns int
	request                          *execution.ExecutionRequest
}

func (e *instrumentedEngine) Initialize(context.Context) error {
	e.initializes++
	if e.mode == "initialize" {
		return errors.New("injected initialize failure")
	}
	return nil
}

func (e *instrumentedEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.executes++
	e.request = req
	switch e.mode {
	case "execution":
		return nil, errors.New("injected execution failure")
	case "nil-response":
		return nil, nil
	case "timeout":
		<-ctx.Done()
		return nil, ctx.Err()
	case "engine-canceled":
		return nil, context.Canceled
	case "unsuccessful-response":
		return &execution.ExecutionResponse{Success: false, FinalOutput: "hello"}, nil
	}
	return &execution.ExecutionResponse{Success: true, FinalOutput: "hello", SessionID: "observed-session"}, nil
}

func (e *instrumentedEngine) Shutdown(context.Context) error {
	e.shutdowns++
	if e.mode == "shutdown" {
		return errors.New("injected shutdown failure")
	}
	return nil
}

func (*instrumentedEngine) SessionUsage(string) *models.UsageStats { return nil }

func TestRunnerActualLifecycleAndNativeRequestPropagation(t *testing.T) {
	for _, mode := range []string{"mock", "native", "initialize", "execution", "nil-response", "timeout", "engine-canceled", "unsuccessful-response", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			request := fixture(t)
			if mode == "native" {
				request.Input.Eval = strings.Replace(request.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
				request.RequiredCapabilities = append(request.RequiredCapabilities, "inline.native-text")
				request.AllowPaidExecution = true
			}
			if mode == "timeout" {
				request.TimeoutMS = 1
			}
			engine := &instrumentedEngine{mode: mode}
			factories := 0
			service := NewService(WithEngineFactory(func(cfg models.Config) (execution.AgentEngine, error) {
				factories++
				require.Equal(t, "mock-model", cfg.ModelID)
				return engine, nil
			}))
			result := service.Run(t.Context(), wire(t, request))
			require.Equal(t, 1, factories)
			require.Equal(t, 1, engine.initializes)
			require.Equal(t, 1, engine.shutdowns)
			executions := 1
			if mode == "initialize" {
				executions = 0
			}
			require.Equal(t, executions, engine.executes)
			expected := InfrastructureFailure
			switch mode {
			case "mock", "native":
				expected = Success
				require.True(t, engine.request.NoSkills)
				require.Empty(t, engine.request.SkillPaths)
				require.Empty(t, engine.request.MCPServers)
				require.Equal(t, "hello", engine.request.Message)
				require.Equal(t, "mock-model", engine.request.ModelID)
				require.Equal(t, 2, result.Observation.RunNumber)
				require.Equal(t, 2, result.Observation.Attempts)
				require.Nil(t, result.Observation.Usage)
			case "timeout":
				expected = Timeout
			case "engine-canceled":
				expected = Canceled
			case "shutdown":
				require.Equal(t, "shutdown", result.Boundary)
				require.NotNil(t, result.Observation, "retain observed checks despite shutdown failure")
			}
			require.Equal(t, expected, result.Outcome, "%+v", result.Diagnostics)
			require.NoError(t, ValidateResult(&result))
		})
	}
}

func TestRunnerNegotiationPrecedesEvenRuntimeConstruction(t *testing.T) {
	req := fixture(t)
	req.RequiredCapabilities = append(req.RequiredCapabilities, "unknown")
	req.Input.Eval = "invalid: source"
	service := NewService(WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
		t.Fatal("incompatibility must reject before constructing any runtime")
		return nil, nil
	}))
	result := service.Run(t.Context(), wire(t, req))
	require.Equal(t, UnsupportedCapability, result.Outcome)
	require.Equal(t, "negotiation", result.Boundary)
}

func TestRunnerPortableSourceBoundsAndResourceConflicts(t *testing.T) {
	for _, fragment := range []string{
		"config:\n  executor: mock\n  model: mock-model\n  trials_per_task: 2147483648\n  timeout_seconds: 30\n",
	} {
		req := fixture(t)
		req.Input.Eval = "schemaVersion: \"2.0\"\nscenario: portable\nname: portable\n" + fragment
		result := NewService().Run(t.Context(), wire(t, req))
		require.Equal(t, InvalidRequest, result.Outcome)
		require.Contains(t, result.Diagnostics[0].Message, "signed 32-bit")
	}
	for _, pair := range [][2]string{{"folder", "folder/file"}, {"same.txt", "SAME.txt"}} {
		req := fixture(t)
		req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello",
			"prompt: hello\n  files:\n    - path: "+pair[0]+"\n      content: a\n    - path: "+pair[1]+"\n      content: b", 1)
		result := NewService().Run(t.Context(), wire(t, req))
		require.Equal(t, InvalidRequest, result.Outcome)
	}
}
