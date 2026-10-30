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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

type publicUnitEngine struct {
	execution.AgentEngine
	initializes, calls, shutdowns atomic.Int32
}

func (e *publicUnitEngine) Initialize(context.Context) error {
	e.initializes.Add(1)
	return errors.New("native initialization must not run")
}

func (e *publicUnitEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls.Add(1)
	return nil, errors.New("native execution must not run")
}

func (e *publicUnitEngine) Shutdown(context.Context) error {
	e.shutdowns.Add(1)
	return errors.New("native shutdown must not run")
}

func TestRunnerExactUnitCLIHTTPConformance(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	var factories atomic.Int32
	service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutesWithService(mux, service)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, action := range []string{"validate", "run"} {
		for _, control := range []string{"good", "conflict", "repeat", "last", "failed-check"} {
			t.Run(action+"/"+control, func(t *testing.T) {
				req := *request
				switch control {
				case "conflict":
					req.Selection.Task = "neighbor"
				case "last":
					req.Selection.Trial, req.Selection.Attempt = 3, 3
				case "failed-check":
					req.Input.Task = strings.Replace(req.Input.Task, "- hello", "- does-not-exist", 1)
				}
				body, err := json.Marshal(req)
				require.NoError(t, err)
				expectedCalls := int32(0)
				if action == "run" && control != "conflict" {
					expectedCalls = 1
				}
				for _, transport := range []string{"cli", "http"} {
					beforeFactories := factories.Load()
					var report runnerunit.Report
					if transport == "cli" {
						cmd := newRunnerCommandWithService(service)
						cmd.SetArgs([]string{action})
						cmd.SetIn(bytes.NewReader(body))
						var output bytes.Buffer
						cmd.SetOut(&output)
						cmd.SetErr(&bytes.Buffer{})
						err := cmd.Execute()
						decoder := json.NewDecoder(&output)
						require.NoError(t, decoder.Decode(&report))
						require.ErrorIs(t, decoder.Decode(new(any)), io.EOF, "one JSON receipt only")
						if report.ExitCode() == 0 {
							require.NoError(t, err)
						} else {
							exit, ok := errors.AsType[*ExitCodeError](err)
							require.True(t, ok)
							require.Equal(t, report.ExitCode(), exit.Code)
						}
					} else {
						response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, response.StatusCode)
						require.NoError(t, json.NewDecoder(response.Body).Decode(&report))
						require.NoError(t, response.Body.Close())
					}
					require.Equal(t, expectedCalls, factories.Load()-beforeFactories, transport)
					require.EqualValues(t, expectedCalls, report.Calls)
					require.False(t, report.AdjacentWork)
					if control == "conflict" {
						require.Equal(t, "rejected", report.Status)
						require.Nil(t, report.Resolved)
						require.Nil(t, report.Observation)
						require.Nil(t, report.Identity)
					} else {
						expected, err := runneridentity.Derive(&req)
						require.NoError(t, err)
						require.Equal(t, expected, report.Identity)
						require.Equal(t, req.Selection, *report.Resolved)
						require.Equal(t, "valid", report.Validation)
						if action == "run" {
							require.Equal(t, "completed", report.Status)
							require.Equal(t, req.Selection.Trial, report.Observation.RunNumber)
							require.Equal(t, req.Selection.Attempt, report.Observation.Attempts)
							require.Contains(t, report.Observation.FinalOutput, "hello")
							if control == "failed-check" {
								require.Equal(t, models.StatusFailed, report.Observation.Status)
								require.Equal(t, 1, report.ExitCode())
							}
						} else {
							require.Equal(t, "ready", report.Status)
							require.Nil(t, report.Observation)
						}
					}
				}
			})
		}
	}
}

func TestRunnerRejectsNonMockFactoryCLIHTTP(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, wrapped := range []bool{false, true} {
		engine := &publicUnitEngine{}
		if wrapped {
			engine.AgentEngine = execution.NewMockEngine("mock")
		}
		service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
			return engine, nil
		})
		cmd := newRunnerCommandWithService(service)
		cmd.SetArgs([]string{"run"})
		cmd.SetIn(bytes.NewReader(data))
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		exit, ok := errors.AsType[*ExitCodeError](err)
		require.True(t, ok)
		require.Equal(t, 2, exit.Code)
		var cliReport runnerunit.Report
		require.NoError(t, json.Unmarshal(output.Bytes(), &cliReport))

		mux := http.NewServeMux()
		webapi.RegisterRunnerRoutesWithService(mux, service)
		server := httptest.NewServer(mux)
		response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var httpReport runnerunit.Report
		require.NoError(t, json.NewDecoder(response.Body).Decode(&httpReport))
		require.NoError(t, response.Body.Close())
		server.Close()
		for _, report := range []runnerunit.Report{cliReport, httpReport} {
			require.Equal(t, "execution_error", report.Status)
			require.Equal(t, "valid", report.Validation)
			require.NotNil(t, report.Identity, "runtime rejection does not erase admitted input identity")
			require.NotNil(t, report.Resolved)
			require.Empty(t, report.Mode)
			require.Zero(t, report.Calls)
			require.Nil(t, report.Observation)
			require.False(t, report.AdjacentWork)
			require.Contains(t, report.Diagnostics[0].Message, "requires a non-nil concrete *execution.MockEngine")
		}
		require.Zero(t, engine.initializes.Load())
		require.Zero(t, engine.calls.Load())
		require.Zero(t, engine.shutdowns.Load())
	}
}

func TestRunnerInlinePromptPresenceCLIHTTP(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	for _, control := range []struct {
		name       string
		inputs     string
		diagnostic string
		valid      bool
		prompt     string
	}{
		{name: "missing-inputs", diagnostic: "requires an explicit inputs.prompt"},
		{name: "empty-inputs", inputs: "inputs: {}\n", diagnostic: "requires an explicit inputs.prompt"},
		{name: "null-inputs", inputs: "inputs: null\n", diagnostic: "must not be null"},
		{name: "scalar-inputs", inputs: "inputs: hello\n", diagnostic: "does not accept a scalar"},
		{name: "sequence-inputs", inputs: "inputs: []\n", diagnostic: "does not accept a sequence"},
		{name: "null-prompt", inputs: "inputs: {prompt: null}\n", diagnostic: "must not be null"},
		{name: "numeric-prompt", inputs: "inputs: {prompt: 123}\n", diagnostic: "requires !!str"},
		{name: "boolean-prompt", inputs: "inputs: {prompt: false}\n", diagnostic: "requires !!str"},
		{name: "mapping-prompt", inputs: "inputs: {prompt: {value: hello}}\n", diagnostic: "does not accept a mapping"},
		{name: "sequence-prompt", inputs: "inputs: {prompt: [hello]}\n", diagnostic: "does not accept a sequence"},
		{name: "duplicate-prompt", inputs: "inputs: {prompt: hello, prompt: other}\n", diagnostic: "unique string mapping keys"},
		{name: "duplicate-inputs", inputs: "inputs: {prompt: hello}\ninputs: {}\n", diagnostic: `mapping key "inputs" already defined`},
		{name: "unexported-prompt-presence-true", inputs: "inputs: {prompt: hello}\ncapturedinlineprompt: true\n", diagnostic: `unknown captured native field "capturedinlineprompt"`},
		{name: "unexported-prompt-presence-false", inputs: "inputs: {prompt: hello}\ncapturedinlineprompt: false\n", diagnostic: `unknown captured native field "capturedinlineprompt"`},
		{name: "unexported-source-document", inputs: "inputs: {prompt: hello}\nsourcedocument: []\n", diagnostic: `unknown captured native field "sourcedocument"`},
		{name: "prompt-file", inputs: "inputs: {prompt_file: absent.txt}\n", diagnostic: "prompt_file cannot read host files"},
		{name: "empty-prompt-file", inputs: "inputs: {prompt_file: ''}\n", diagnostic: "requires an explicit inputs.prompt"},
		{name: "empty-prompt-with-file", inputs: "inputs: {prompt: '', prompt_file: absent.txt}\n", diagnostic: "prompt_file cannot read host files"},
		{name: "empty-prompt", inputs: "inputs: {prompt: ''}\n", valid: true},
		{name: "populated-prompt", inputs: "inputs: {prompt: hello}\n", valid: true, prompt: "hello"},
	} {
		t.Run(control.name, func(t *testing.T) {
			req := *request
			req.Input.Task = "id: " + req.Selection.Task + "\nname: Prompt presence\n" + control.inputs +
				"expected:\n  output_contains: ['Mock response for:']\n"
			body, err := json.Marshal(req)
			require.NoError(t, err)
			var factories atomic.Int32
			service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				factories.Add(1)
				return execution.NewMockEngine(cfg.ModelID), nil
			})
			mux := http.NewServeMux()
			webapi.RegisterRunnerRoutesWithService(mux, service)
			server := httptest.NewServer(mux)
			defer server.Close()
			for _, action := range []string{"validate", "run"} {
				for _, transport := range []string{"cli", "http"} {
					t.Run(action+"/"+transport, func(t *testing.T) {
						before := factories.Load()
						var report runnerunit.Report
						if transport == "cli" {
							cmd := newRunnerCommandWithService(service)
							cmd.SetArgs([]string{action})
							cmd.SetIn(bytes.NewReader(body))
							var output bytes.Buffer
							cmd.SetOut(&output)
							cmd.SetErr(&bytes.Buffer{})
							err := cmd.Execute()
							decoder := json.NewDecoder(&output)
							require.NoError(t, decoder.Decode(&report))
							require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
							if control.valid {
								require.NoError(t, err)
							} else {
								exit, ok := errors.AsType[*ExitCodeError](err)
								require.True(t, ok, "invalid captured prompt must fail the CLI")
								require.Equal(t, 6, exit.Code)
							}
						} else {
							response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
							require.NoError(t, err)
							require.Equal(t, http.StatusOK, response.StatusCode, "admission rejection is a runner receipt")
							decoder := json.NewDecoder(response.Body)
							require.NoError(t, decoder.Decode(&report))
							require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
							require.NoError(t, response.Body.Close())
						}
						require.Equal(t, "compatible", report.Compatibility.Status)
						require.Equal(t, req.Selection, *report.Requested)
						require.False(t, report.AdjacentWork)
						if !control.valid {
							require.Equal(t, "rejected", report.Status)
							require.Equal(t, "invalid", report.Validation)
							require.Equal(t, 6, report.ExitCode())
							require.Nil(t, report.Resolved)
							require.Nil(t, report.Observation)
							require.Nil(t, report.Identity)
							require.Empty(t, report.Mode)
							require.Zero(t, report.Calls)
							require.Equal(t, before, factories.Load(), "reject before runtime construction")
							require.Len(t, report.Diagnostics, 1)
							require.Equal(t, "invalid_selection", report.Diagnostics[0].Code)
							require.Contains(t, report.Diagnostics[0].Message, control.diagnostic)
							return
						}
						require.Equal(t, "valid", report.Validation)
						require.Equal(t, req.Selection, *report.Resolved)
						require.Empty(t, report.Diagnostics)
						if action == "validate" {
							require.Equal(t, "ready", report.Status)
							require.Nil(t, report.Observation)
							require.Empty(t, report.Mode)
							require.Zero(t, report.Calls)
							require.Equal(t, before, factories.Load(), "validation stays offline")
						} else {
							require.Equal(t, "completed", report.Status)
							require.Equal(t, "mock", report.Mode)
							require.Equal(t, before+1, factories.Load())
							require.Equal(t, 1, report.Calls)
							require.NotNil(t, report.Observation)
							require.Equal(t, models.StatusPassed, report.Observation.Status)
							require.Equal(t, "Mock response for: "+control.prompt+"\nTask: Prompt presence", report.Observation.FinalOutput)
						}
					})
				}
			}
		})
	}
}

func TestRunnerUnitInputCancellationAndFailures(t *testing.T) {
	for _, action := range []string{"validate", "run"} {
		t.Run(action, func(t *testing.T) {
			input, producer := io.Pipe()
			defer input.Close()    //nolint:errcheck
			defer producer.Close() //nolint:errcheck
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := newRunnerCommandWithService(runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("canceled input constructed runtime")
				return nil, nil
			}))
			cmd.SetArgs([]string{action})
			cmd.SetIn(input)
			cmd.SetErr(&bytes.Buffer{})
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			_, err := producer.Write([]byte("{"))
			require.NoError(t, err, "read is active while producer stays open")
			cancel()
			select {
			case err := <-done:
				require.ErrorContains(t, err, "runner input interrupted")
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation waited for producer EOF")
			}
			for _, input := range []io.Reader{errorReader{}, strings.NewReader("{}")} {
				cmd := newRunnerCommand()
				cmd.SetArgs([]string{action})
				cmd.SetIn(input)
				cmd.SetOut(errorWriter{})
				cmd.SetErr(&bytes.Buffer{})
				require.Error(t, cmd.Execute())
			}
		})
	}
}
