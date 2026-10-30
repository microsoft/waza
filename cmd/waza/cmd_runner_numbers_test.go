package main

import (
	"bytes"
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

func TestRunnerIntegerSpellingsCLIHTTP(t *testing.T) {
	fixture, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(fixture)
	require.NoError(t, err)
	// Large selectors still execute only one attempt, not the preceding work.
	request.Input.Eval = strings.ReplaceAll(request.Input.Eval, ": 3\n", ": 2147483647\n")
	require.Contains(t, request.Input.Eval, "trials_per_task: 2147483647\n")
	require.Contains(t, request.Input.Eval, "max_attempts: 2147483647\n")
	base, err := json.Marshal(request)
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

	for _, field := range []string{"timeout_ms", "trial", "attempt"} {
		limit, canonical := "2147483648", "2"
		valid := []string{"2.0", "20e-1", "2e0", "2.147483647e9"}
		if field == "timeout_ms" {
			limit, canonical = "86400001", "60000"
			valid = []string{"6e4", "60000.0", "600000e-1", "8.64e7"}
		}
		for _, token := range append(append([]string{canonical}, valid...),
			"2.0000000000000001", "21e-1", limit, "9223372036854775808",
			"1e100", "0", "-1", "null", `"2"`, "true", "[]", "{}") {
			t.Run(field+"/"+token, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(base, &fields))
				target := fields
				if field != "timeout_ms" {
					target = make(map[string]json.RawMessage)
					require.NoError(t, json.Unmarshal(fields["selection"], &target))
				}
				target[field] = json.RawMessage(token)
				if field != "timeout_ms" {
					fields["selection"], err = json.Marshal(target)
					require.NoError(t, err)
				}
				body, err := json.Marshal(fields)
				require.NoError(t, err)
				require.Contains(t, string(body), `"`+field+`":`+token, "retain the original numeric token")
				accepted := token == canonical
				for _, candidate := range valid {
					accepted = accepted || token == candidate
				}
				expected := request.Selection
				if accepted && field != "timeout_ms" {
					want := 2
					if token == "2.147483647e9" {
						want = 2147483647
					}
					if field == "trial" {
						expected.Trial = want
					} else {
						expected.Attempt = want
					}
				}
				for _, action := range []string{"negotiate", "validate", "run"} {
					for _, transport := range []string{"cli", "http"} {
						t.Run(action+"/"+transport, func(t *testing.T) {
							before := factories.Load()
							output, exitCode := invokeRunnerNumbers(t, service, server, transport, action, body)
							if action == "negotiate" {
								var report runnercontract.Response
								require.NoError(t, json.Unmarshal(output, &report))
								require.Equal(t, before, factories.Load())
								if accepted {
									require.Equal(t, "compatible", report.Status)
									require.Zero(t, exitCode)
								} else {
									require.Equal(t, "invalid_request", report.Status)
									require.Equal(t, 6, report.ExitCode())
									require.NotEmpty(t, report.Diagnostics)
								}
								return
							}
							var report runnerunit.Report
							require.NoError(t, json.Unmarshal(output, &report))
							require.False(t, report.AdjacentWork)
							if !accepted {
								require.Equal(t, "rejected", report.Status)
								require.Equal(t, 6, report.ExitCode())
								require.Nil(t, report.Identity)
								require.Nil(t, report.Resolved)
								require.Nil(t, report.Observation)
								require.Zero(t, report.Calls)
								require.Equal(t, before, factories.Load())
								return
							}
							require.Zero(t, exitCode)
							require.Equal(t, "valid", report.Validation)
							require.Equal(t, expected, *report.Requested)
							require.Equal(t, expected, *report.Resolved)
							if action == "validate" {
								require.Equal(t, "ready", report.Status)
								require.Nil(t, report.Observation)
								require.Zero(t, report.Calls)
								require.Equal(t, before, factories.Load())
							} else {
								require.Equal(t, "completed", report.Status)
								require.Equal(t, 1, report.Calls)
								require.Equal(t, before+1, factories.Load())
								require.NotNil(t, report.Observation)
								require.Equal(t, expected.Trial, report.Observation.RunNumber)
								require.Equal(t, expected.Attempt, report.Observation.Attempts)
								require.Equal(t, models.StatusPassed, report.Observation.Status)
								require.Contains(t, report.Observation.FinalOutput, "hello")
							}
						})
					}
				}
				if accepted {
					parsed, err := runnercontract.Parse(body)
					require.NoError(t, err)
					require.Equal(t, expected, parsed.Selection)
					want := 60000
					if token == "8.64e7" {
						want = 86400000
					}
					require.Equal(t, want, parsed.TimeoutMS)
				}
			})
		}
	}
}

func TestRunnerIntegerTimeoutCLIHTTP(t *testing.T) {
	fixture, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, token := range []string{"2", "2.0", "20e-1", "2e0"} {
		t.Run(token, func(t *testing.T) {
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(fixture, &fields))
			fields["timeout_ms"] = json.RawMessage(token)
			body, err := json.Marshal(fields)
			require.NoError(t, err)
			require.Contains(t, string(body), `"timeout_ms":`+token)
			var factories atomic.Int32
			service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				factories.Add(1)
				// Return only after the admitted 2 ms execution deadline.
				<-time.After(20 * time.Millisecond)
				return execution.NewMockEngine(cfg.ModelID), nil
			})
			mux := http.NewServeMux()
			webapi.RegisterRunnerRoutesWithService(mux, service)
			server := httptest.NewServer(mux)
			defer server.Close()
			for _, action := range []string{"negotiate", "validate", "run"} {
				for _, transport := range []string{"cli", "http"} {
					t.Run(action+"/"+transport, func(t *testing.T) {
						before := factories.Load()
						output, exitCode := invokeRunnerNumbers(t, service, server, transport, action, body)
						if action == "negotiate" {
							var report runnercontract.Response
							require.NoError(t, json.Unmarshal(output, &report))
							require.Equal(t, "compatible", report.Status)
							require.Zero(t, exitCode)
							require.Equal(t, before, factories.Load())
							return
						}
						var report runnerunit.Report
						require.NoError(t, json.Unmarshal(output, &report))
						require.Equal(t, "valid", report.Validation)
						require.NotNil(t, report.Resolved)
						require.Zero(t, report.Calls)
						require.Nil(t, report.Observation)
						if action == "validate" {
							require.Equal(t, "ready", report.Status)
							require.Zero(t, exitCode)
							require.Equal(t, before, factories.Load())
						} else {
							require.Equal(t, "execution_error", report.Status)
							require.Equal(t, 2, report.ExitCode())
							if transport == "cli" {
								require.Equal(t, 2, exitCode)
							}
							require.Equal(t, before+1, factories.Load())
							require.Len(t, report.Diagnostics, 1)
							require.Contains(t, report.Diagnostics[0].Message, "context deadline exceeded")
						}
					})
				}
			}
		})
	}
}

func invokeRunnerNumbers(t *testing.T, service *runnerunit.Service, server *httptest.Server, transport, action string, body []byte) ([]byte, int) {
	t.Helper()
	var output []byte
	exitCode := 0
	if transport == "cli" {
		cmd := newRunnerCommandWithService(service)
		cmd.SetArgs([]string{action})
		cmd.SetIn(bytes.NewReader(body))
		var buffer bytes.Buffer
		cmd.SetOut(&buffer)
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			exit, ok := errors.AsType[*ExitCodeError](err)
			require.True(t, ok, "%v", err)
			exitCode = exit.Code
		}
		output = buffer.Bytes()
	} else {
		response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		output, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	require.NoError(t, decoder.Decode(new(any)))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF, "exactly one JSON receipt")
	if transport == "cli" {
		var status struct {
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(output, &status))
		if status.Status == "invalid_request" || status.Status == "rejected" {
			require.Equal(t, 6, exitCode)
		}
	}
	return output, exitCode
}
