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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
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
							if action == "run" {
								admitted := runnerunit.Validate(body)
								var binding *runnerterminal.Binding
								if accepted {
									require.Equal(t, "ready", admitted.Status)
									binding = &runnerterminal.Binding{Selection: expected, Identity: admitted.Identity,
										GraderIDs: []string{"_output_contains"}}
								}
								envelope, err := runnerterminal.Validate(output, binding)
								require.NoError(t, err, "%s", output)
								if !accepted {
									require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
									require.Equal(t, 6, envelope.ExitCode)
									require.NotEmpty(t, envelope.Diagnostics)
									require.Nil(t, envelope.Identity)
									require.Nil(t, envelope.Selection)
									require.Nil(t, envelope.Observation)
									require.Zero(t, envelope.EngineCalls)
									require.Equal(t, before, factories.Load())
									return
								}
								require.Zero(t, exitCode)
								require.Equal(t, runnerterminal.Success, envelope.Outcome)
								require.Zero(t, envelope.ExitCode)
								require.Equal(t, "valid", envelope.Validation)
								require.Empty(t, envelope.Diagnostics)
								require.Equal(t, expected, *envelope.Selection)
								require.Equal(t, admitted.Identity, envelope.Identity)
								require.Equal(t, 1, envelope.EngineCalls)
								require.Equal(t, before+1, factories.Load())
								require.NotNil(t, envelope.Observation)
								require.Equal(t, expected, envelope.Observation.Selection)
								require.Equal(t, expected.Trial, envelope.Observation.RunNumber)
								require.Equal(t, expected.Attempt, envelope.Observation.Attempts)
								require.Equal(t, "passed", envelope.Observation.Status)
								require.Equal(t, "mock", envelope.Observation.ExecutionMode)
								require.Equal(t, 1, envelope.Observation.EngineCalls)
								require.Equal(t, []runnerterminal.Verdict{{Identifier: "_output_contains", Passed: true}},
									envelope.Observation.Validations)
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
							require.Equal(t, "ready", report.Status)
							require.Nil(t, report.Observation)
							require.Zero(t, report.Calls)
							require.Equal(t, before, factories.Load())
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
						if action == "run" {
							admitted := runnerunit.Validate(body)
							require.Equal(t, "ready", admitted.Status)
							envelope, err := runnerterminal.Validate(output, &runnerterminal.Binding{
								Selection: *admitted.Resolved, Identity: admitted.Identity, GraderIDs: []string{"_output_contains"},
							})
							require.NoError(t, err)
							require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
							require.Equal(t, 4, envelope.ExitCode)
							require.Equal(t, "startup", envelope.FailureBoundary)
							require.Equal(t, "valid", envelope.Validation)
							require.Equal(t, admitted.Resolved, envelope.Selection)
							require.Equal(t, admitted.Identity, envelope.Identity)
							require.Zero(t, envelope.EngineCalls)
							require.Nil(t, envelope.Observation)
							if transport == "cli" {
								require.Equal(t, 4, exitCode)
							}
							constructed := factories.Load() - before
							require.Contains(t, []int32{0, 1}, constructed,
								"startup may expire before construction or during the synchronous factory")
							require.Len(t, envelope.Diagnostics, 1)
							require.Contains(t, envelope.Diagnostics[0].Message, "context deadline exceeded")
							return
						}
						var report runnerunit.Report
						require.NoError(t, json.Unmarshal(output, &report))
						require.Equal(t, "valid", report.Validation)
						require.NotNil(t, report.Resolved)
						require.Zero(t, report.Calls)
						require.Nil(t, report.Observation)
						require.Equal(t, "ready", report.Status)
						require.Zero(t, exitCode)
						require.Equal(t, before, factories.Load())
					})
				}
			}
		})
	}
}

func TestRunnerIntegerTimeoutStartupBranches(t *testing.T) {
	fixture, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, token := range []string{"2", "2.0", "20e-1", "2e0"} {
		for _, entered := range []bool{false, true} {
			t.Run(token+"/factory-entered="+strconv.FormatBool(entered), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(fixture, &fields))
					fields["timeout_ms"] = json.RawMessage(token)
					body, err := json.Marshal(fields)
					require.NoError(t, err)
					require.Contains(t, string(body), `"timeout_ms":`+token)
					admitted := runnerunit.Validate(body)
					require.Equal(t, "ready", admitted.Status)
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Millisecond)
					defer cancel()
					start := time.Now()
					calls := 0
					service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
						calls++
						<-ctx.Done()
						// Parent Done closes before cancellation reaches the service's child.
						synctest.Wait()
						return execution.NewMockEngine(cfg.ModelID), nil
					})
					if !entered {
						<-ctx.Done()
					}
					envelope, binding := service.RunTerminal(ctx, body)
					require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
					require.Equal(t, 2*time.Millisecond, time.Since(start), "advance only the actual virtual deadline")
					wantCalls := 0
					if entered {
						wantCalls = 1
					}
					require.Equal(t, wantCalls, calls)
					var output bytes.Buffer
					code, err := runnerterminal.NewEmitter(&output).Emit(envelope, binding)
					require.NoError(t, err)
					require.Equal(t, 4, code)
					decoded, err := runnerterminal.Validate(output.Bytes(), &runnerterminal.Binding{
						Selection: *admitted.Resolved, Identity: admitted.Identity, GraderIDs: []string{"_output_contains"},
					})
					require.NoError(t, err)
					require.Equal(t, runnerterminal.Timeout, decoded.Outcome)
					require.Equal(t, "startup", decoded.FailureBoundary)
					require.Equal(t, "valid", decoded.Validation)
					require.Equal(t, admitted.Resolved, decoded.Selection)
					require.Equal(t, admitted.Identity, decoded.Identity)
					require.Zero(t, decoded.EngineCalls)
					require.Nil(t, decoded.Observation)
					require.Len(t, decoded.Diagnostics, 1)
					require.Contains(t, decoded.Diagnostics[0].Message, "context deadline exceeded")
				})
			})
		}
	}
}

func TestRunnerIntegerTimeoutStartupCLI(t *testing.T) {
	fixture, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	// The process-wide signal runtime must not be initialized inside a bubble.
	_, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	stop()
	for _, token := range []string{"2", "2.0", "20e-1", "2e0"} {
		for _, deadline := range []string{"parent", "service", "active"} {
			for _, factoryError := range []bool{false, true} {
				t.Run(token+"/"+deadline+"/factory-error="+strconv.FormatBool(factoryError), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						var fields map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(fixture, &fields))
						fields["timeout_ms"] = json.RawMessage(token)
						body, err := json.Marshal(fields)
						require.NoError(t, err)
						require.Contains(t, string(body), `"timeout_ms":`+token)
						admitted := runnerunit.Validate(body)
						require.Equal(t, "ready", admitted.Status)
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()
						if deadline == "parent" {
							var stop context.CancelFunc
							ctx, stop = context.WithTimeout(ctx, 2*time.Millisecond)
							defer stop()
						}
						start := time.Now()
						calls := 0
						service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
							calls++
							switch deadline {
							case "parent":
								<-ctx.Done()
							case "service":
								// Advance exactly the request budget, not the parent deadline.
								time.Sleep(2 * time.Millisecond)
							}
							// Drain cancellation through the CLI and service-owned contexts.
							synctest.Wait()
							if factoryError {
								return nil, errors.New("injected startup factory failure")
							}
							return execution.NewMockEngine(cfg.ModelID), nil
						})
						command := newRunnerCommandWithService(service)
						command.SetArgs([]string{"run"})
						command.SetIn(bytes.NewReader(body))
						var output bytes.Buffer
						command.SetOut(&output)
						command.SetErr(&bytes.Buffer{})
						commandErr := command.ExecuteContext(ctx)
						require.Equal(t, 1, calls, "the admitted factory must actually be entered")
						if deadline == "parent" {
							require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
						} else {
							require.NoError(t, ctx.Err(), "do not borrow the parent's error for the service deadline")
						}
						decoded, err := runnerterminal.Validate(output.Bytes(), &runnerterminal.Binding{
							Selection: *admitted.Resolved, Identity: admitted.Identity, GraderIDs: []string{"_output_contains"},
						})
						require.NoError(t, err, "%s", output.Bytes())
						require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
						require.Equal(t, "valid", decoded.Validation)
						require.Equal(t, admitted.Resolved, decoded.Selection)
						require.Equal(t, admitted.Identity, decoded.Identity)
						if deadline == "active" && !factoryError {
							require.NoError(t, commandErr)
							require.Equal(t, runnerterminal.Success, decoded.Outcome)
							require.Zero(t, decoded.ExitCode)
							require.Equal(t, 1, decoded.EngineCalls)
							require.NotNil(t, decoded.Observation)
							require.Empty(t, decoded.Diagnostics)
							require.Zero(t, time.Since(start))
							return
						}
						want, code, elapsed := runnerterminal.Timeout, 4, 2*time.Millisecond
						if deadline == "active" {
							want, code, elapsed = runnerterminal.InfrastructureFailure, 2, 0
						}
						exit, ok := errors.AsType[*ExitCodeError](commandErr)
						require.True(t, ok, "%v", commandErr)
						require.Equal(t, code, exit.Code)
						require.Equal(t, code, decoded.ExitCode)
						require.Equal(t, want, decoded.Outcome)
						require.Equal(t, "startup", decoded.FailureBoundary)
						require.Zero(t, decoded.EngineCalls)
						require.Nil(t, decoded.Observation)
						require.Len(t, decoded.Diagnostics, 1)
						require.Equal(t, elapsed, time.Since(start))
						if deadline != "active" {
							require.Contains(t, decoded.Diagnostics[0].Message, "context deadline exceeded")
						}
						if factoryError {
							require.Contains(t, decoded.Diagnostics[0].Message, "injected startup factory failure")
						}
					})
				})
			}
		}
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
		if action == "run" {
			var envelope runnerterminal.Envelope
			require.NoError(t, json.Unmarshal(output, &envelope))
			require.Equal(t, strconv.Itoa(envelope.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
		}
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
