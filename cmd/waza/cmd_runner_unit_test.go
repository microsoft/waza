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
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

type publicUnitEngine struct {
	execution.AgentEngine
	calls *atomic.Int32
}

func (e publicUnitEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls.Add(1)
	return e.AgentEngine.Execute(ctx, req)
}

func TestRunnerExactUnitCLIHTTPConformance(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	var calls, factories atomic.Int32
	service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return publicUnitEngine{execution.NewMockEngine(cfg.ModelID), &calls}, nil
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
					beforeCalls, beforeFactories := calls.Load(), factories.Load()
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
					require.Equal(t, expectedCalls, calls.Load()-beforeCalls, transport)
					require.Equal(t, expectedCalls, factories.Load()-beforeFactories, transport)
					require.EqualValues(t, expectedCalls, report.Calls)
					require.False(t, report.AdjacentWork)
					if control == "conflict" {
						require.Equal(t, "rejected", report.Status)
						require.Nil(t, report.Resolved)
						require.Nil(t, report.Observation)
					} else {
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
