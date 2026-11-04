package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

type terminalDeadlineTrace struct {
	http.ResponseWriter
	writes []time.Time
}

func (w *terminalDeadlineTrace) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *terminalDeadlineTrace) SetWriteDeadline(deadline time.Time) error {
	w.writes = append(w.writes, deadline)
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func terminalProtocolServer(t *testing.T, handler http.Handler, protocol int) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	if protocol == 2 {
		server.EnableHTTP2 = true
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	server.Client().Timeout = 15 * time.Second
	return server
}

// Hold the concrete mock's existing serialization mutex inside a separate,
// synthetic Execute using its public capture callback. The admitted invocation
// must then enter its own real Execute before waiting for this contention.
// Neither the factory nor an engine wrapper supplies the execution delay.
func terminalContendedMock(t *testing.T) (*execution.MockEngine, func()) {
	t.Helper()
	engine := execution.NewMockEngine("mock-model")
	require.NoError(t, engine.Initialize(t.Context()))
	holding := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	workspace := t.TempDir()
	go func() {
		_, err := engine.Execute(t.Context(), &execution.ExecutionRequest{
			Message:      "separate synthetic contention, not the admitted request",
			WorkspaceDir: workspace,
			CaptureJudgeFiles: func(string) *models.JudgeFileCapture {
				close(holding)
				<-release
				return nil
			},
		})
		finished <- err
	}()
	select {
	case <-holding:
	case err := <-finished:
		t.Fatalf("synthetic Execute exited before the capture barrier: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic Execute did not reach the lock-held capture barrier")
	}
	var joined sync.Once
	return engine, func() {
		joined.Do(func() {
			unlock()
			require.NoError(t, <-finished)
		})
	}
}

func terminalAwaitProductionExecute(t *testing.T) time.Time {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buffer := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buffer, true)
		require.Less(t, n, len(buffer), "phase evidence must not use a truncated goroutine dump")
		for _, stack := range strings.Split(string(buffer[:n]), "\n\n") {
			if strings.Contains(stack, "execution.(*MockEngine).Execute(") &&
				strings.Contains(stack, "runnerunit.(*onceEngine).Execute(") &&
				strings.Contains(stack, "webapi.runTerminal(") &&
				strings.Contains(stack, "sync.(*Mutex).") {
				t.Logf("production handler is inside concrete mock Execute under synthetic contention:\n%s", stack)
				return time.Now()
			}
		}
		runtime.Gosched()
	}
	t.Fatal("registered production handler did not enter concrete mock Execute")
	return time.Time{}
}

func TestRunnerTerminalExecutionDeadlinePhases(t *testing.T) {
	for _, protocol := range []int{2, 1} {
		name := "http2"
		if protocol == 1 {
			name = "http1"
		}
		t.Run(name, func(t *testing.T) {
			for _, control := range []struct {
				name      string
				hold      time.Duration
				requestMS int
				task      string
				cancel    bool
				outcome   runnerterminal.Outcome
				exit      int
			}{
				{"long-evaluation", 5200 * time.Millisecond, 20000, "timeout_seconds: 10\n", false, runnerterminal.Success, 0},
				{"short-evaluation", 0, 20000, "", false, runnerterminal.Success, 0},
				{"request-timeout", 1100 * time.Millisecond, 1000, "", false, runnerterminal.Timeout, 4},
				{"task-timeout", 1100 * time.Millisecond, 20000, "timeout_seconds: 1\n", false, runnerterminal.Timeout, 4},
				{"server-cancellation", 0, 20000, "", true, runnerterminal.Canceled, 3},
			} {
				t.Run(control.name, func(t *testing.T) {
					source, err := os.ReadFile("../../examples/runner/unit.json")
					require.NoError(t, err)
					var request runnercontract.Request
					require.NoError(t, json.Unmarshal(source, &request))
					request.TimeoutMS = control.requestMS
					request.Input.Task += control.task
					if control.name == "short-evaluation" {
						request.Selection.Trial, request.Selection.Attempt = 1, 1
					}
					data, err := json.Marshal(request)
					require.NoError(t, err)
					admitted := runnerunit.Validate(data)
					require.Equal(t, "ready", admitted.Status, "%+v", admitted)
					require.NotNil(t, admitted.Resolved)
					require.NotNil(t, admitted.Identity)
					binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
						GraderIDs: []string{"_output_contains"}}
					engine, release := terminalContendedMock(t)
					var factories atomic.Int32
					mux := http.NewServeMux()
					RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
						factories.Add(1)
						if cfg.ModelID != "mock-model" {
							return nil, errors.New("unexpected admitted model")
						}
						return engine, nil
					}))
					type invocation struct {
						protocol int
						cancel   context.CancelFunc
					}
					started := make(chan invocation, 1)
					finished := make(chan []time.Time, 1)
					server := terminalProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ctx, cancel := context.WithCancel(r.Context())
						defer cancel()
						started <- invocation{r.ProtoMajor, cancel}
						trace := &terminalDeadlineTrace{ResponseWriter: w}
						mux.ServeHTTP(trace, r.WithContext(ctx))
						finished <- trace.writes
					}), protocol)
					t.Cleanup(release)
					type reply struct {
						response *http.Response
						err      error
					}
					replies := make(chan reply, 1)
					go func() {
						response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(data))
						replies <- reply{response, err}
					}()
					active := <-started
					require.Equal(t, protocol, active.protocol, "verify actual negotiated handler protocol before testing its deadline")
					insideExecute := terminalAwaitProductionExecute(t)
					if control.cancel {
						active.cancel()
					}
					if control.hold > 0 {
						<-time.NewTimer(time.Until(insideExecute.Add(control.hold))).C
					}
					elapsed := time.Since(insideExecute)
					if control.name == "long-evaluation" {
						require.Greater(t, elapsed, 5*time.Second, "real production Execute must span the original write deadline")
						t.Logf("concrete production Execute still contended after %s (> original 5s deadline)", elapsed)
					}
					release()
					got := <-replies
					require.NoError(t, got.err, "terminal delivery after %s inside real Execute", elapsed)
					require.Equal(t, protocol, got.response.ProtoMajor)
					if protocol == 2 {
						require.NotNil(t, got.response.TLS)
						require.Equal(t, "h2", got.response.TLS.NegotiatedProtocol)
					}
					require.Equal(t, http.StatusOK, got.response.StatusCode)
					require.Equal(t, strconv.Itoa(control.exit), got.response.Header.Get("X-Waza-Exit-Code"))
					body, err := io.ReadAll(got.response.Body)
					require.NoError(t, err)
					require.NoError(t, got.response.Body.Close())
					envelope, err := runnerterminal.Validate(body, binding)
					require.NoError(t, err, "%s", body)
					require.Equal(t, control.outcome, envelope.Outcome, "%s", body)
					require.Equal(t, control.exit, envelope.ExitCode)
					require.Equal(t, binding.Identity, envelope.Identity)
					require.Equal(t, binding.Selection, *envelope.Selection)
					require.Equal(t, 1, envelope.EngineCalls)
					require.NotNil(t, envelope.Observation)
					require.Equal(t, binding.Selection, envelope.Observation.Selection)
					require.Equal(t, binding.Selection.Trial, envelope.Observation.RunNumber)
					require.Equal(t, binding.Selection.Attempt, envelope.Observation.Attempts)
					require.Equal(t, 1, envelope.Observation.EngineCalls)
					if control.outcome == runnerterminal.Success {
						require.Equal(t, "passed", envelope.Observation.Status)
						require.Len(t, envelope.Observation.Validations, 1)
						require.Equal(t, "_output_contains", envelope.Observation.Validations[0].Identifier)
						require.True(t, envelope.Observation.Validations[0].Passed)
					} else {
						require.Equal(t, "execution", envelope.FailureBoundary)
						require.Equal(t, "error", envelope.Observation.Status)
						require.NotEmpty(t, envelope.Observation.ErrorMsg)
					}
					require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
					require.Equal(t, int32(1), factories.Load())
					deadlines := <-finished
					require.Len(t, deadlines, 2, "one support probe and one output deadline")
					require.True(t, deadlines[0].IsZero(), "capability admission must not arm a stream timer")
					require.WithinDuration(t, insideExecute.Add(elapsed+5*time.Second), deadlines[1], time.Second)
				})
			}
		})
	}
}

func TestRunnerTerminalHTTP2InputDeadlineRetainsEnvelope(t *testing.T) {
	var factories atomic.Int32
	mux := http.NewServeMux()
	RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return nil, errors.New("partial input must not construct an engine")
	}))
	started := make(chan int, 1)
	finished := make(chan []time.Time, 1)
	server := terminalProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.ProtoMajor
		trace := &terminalDeadlineTrace{ResponseWriter: w}
		mux.ServeHTTP(trace, r)
		finished <- trace.writes
	}), 2)
	reader, writer := io.Pipe()
	defer reader.Close() //nolint:errcheck
	defer writer.Close() //nolint:errcheck
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/runner/run", reader)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = 100
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("{"))
		written <- err
	}()
	start := time.Now()
	response, err := server.Client().Do(request)
	require.NoError(t, err, "body timeout must not also expire the HTTP/2 output stream")
	require.NoError(t, <-written)
	require.Equal(t, 2, <-started)
	require.Equal(t, 2, response.ProtoMajor)
	require.NotNil(t, response.TLS)
	require.Equal(t, "h2", response.TLS.NegotiatedProtocol)
	require.GreaterOrEqual(t, time.Since(start), 5*time.Second)
	require.Equal(t, http.StatusRequestTimeout, response.StatusCode)
	require.Equal(t, "4", response.Header.Get("X-Waza-Exit-Code"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	envelope, err := runnerterminal.Validate(body, nil)
	require.NoError(t, err, "%s", body)
	require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
	require.Equal(t, "input", envelope.FailureBoundary)
	require.Nil(t, envelope.Identity)
	require.Nil(t, envelope.Observation)
	require.Zero(t, envelope.EngineCalls)
	require.Zero(t, factories.Load())
	require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
	deadlines := <-finished
	require.Len(t, deadlines, 2)
	require.True(t, deadlines[0].IsZero())
	require.False(t, deadlines[1].IsZero())
}

type terminalWriteProbeFailure struct {
	http.ResponseWriter
	writes      int
	bodyCalls   int
	headerCalls int
	unsupported bool
}

func (w *terminalWriteProbeFailure) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *terminalWriteProbeFailure) Write(data []byte) (int, error) {
	w.bodyCalls++
	return w.ResponseWriter.Write(data)
}

func (w *terminalWriteProbeFailure) WriteHeader(status int) {
	w.headerCalls++
	w.ResponseWriter.WriteHeader(status)
}

func (w *terminalWriteProbeFailure) SetWriteDeadline(deadline time.Time) error {
	w.writes++
	if w.writes == 1 || w.unsupported {
		return http.ErrNotSupported
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func TestRunnerTerminalWriteDeadlineAdmissionFailsClosed(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, protocol := range []int{2, 1} {
		for _, unsupported := range []bool{false, true} {
			t.Run(strconv.Itoa(protocol)+"/unsupported="+strconv.FormatBool(unsupported), func(t *testing.T) {
				var factories atomic.Int32
				mux := http.NewServeMux()
				RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					factories.Add(1)
					return nil, errors.New("undeliverable input must not construct an engine")
				}))
				finished := make(chan *terminalWriteProbeFailure, 1)
				server := terminalProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					output := &terminalWriteProbeFailure{ResponseWriter: w, unsupported: unsupported}
					defer func() { finished <- output }()
					mux.ServeHTTP(output, r)
				}), protocol)
				response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(request))
				output := <-finished
				require.Equal(t, 2, output.writes, "one probe and one rejection delivery deadline; never retry")
				require.Zero(t, factories.Load())
				if unsupported {
					require.Error(t, err)
					require.Nil(t, response)
					require.Zero(t, output.bodyCalls)
					require.Zero(t, output.headerCalls)
					return
				}
				require.NoError(t, err)
				require.Equal(t, protocol, response.ProtoMajor)
				require.Equal(t, http.StatusInternalServerError, response.StatusCode)
				require.Equal(t, "2", response.Header.Get("X-Waza-Exit-Code"))
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				envelope, err := runnerterminal.Validate(body, nil)
				require.NoError(t, err, "%s", body)
				require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
				require.Equal(t, "output", envelope.FailureBoundary)
				require.Nil(t, envelope.Identity)
				require.Nil(t, envelope.Observation)
				require.Zero(t, envelope.EngineCalls)
				require.Zero(t, factories.Load())
				require.Contains(t, envelope.Diagnostics[0].Message, "Probing runner write deadline support: feature not supported")
				require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
				require.Len(t, envelope.Diagnostics, 1)
				require.Equal(t, 1, output.bodyCalls)
				require.Equal(t, 1, output.headerCalls)
			})
		}
	}
}

func TestRunnerTerminalHTTP2LateOutputFailure(t *testing.T) {
	testRunnerTerminalLateOutputFailure(t, 2)
}

func TestRunnerTerminalHTTP2OutputFailuresNeverRetry(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.Equal(t, "ready", admitted.Status)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
		GraderIDs: []string{"_output_contains"}}
	for _, control := range []string{"short", "write", "flush"} {
		t.Run(control, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterRunnerRoutes(mux)
			finished := make(chan *terminalTransportFailure, 1)
			server := terminalProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				output := &terminalTransportFailure{ResponseWriter: w, control: control}
				mux.ServeHTTP(output, r)
				finished <- output
			}), 2)
			response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(request))
			require.NoError(t, err)
			require.Equal(t, 2, response.ProtoMajor)
			require.Equal(t, "0", response.Header.Get("X-Waza-Exit-Code"))
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			output := <-finished
			require.Equal(t, 1, output.calls)
			envelope, err := runnerterminal.Validate(output.attempt, binding)
			require.NoError(t, err, "%s", output.attempt)
			require.Equal(t, runnerterminal.Success, envelope.Outcome)
			require.Equal(t, 1, envelope.EngineCalls)
			if control == "flush" {
				require.Equal(t, 1, output.flushes)
				require.Equal(t, output.attempt, body)
			} else {
				require.Zero(t, output.flushes)
				require.Equal(t, output.attempt[:len(output.attempt)/2], body)
				require.False(t, json.Valid(body), "failed partial delivery must not become a valid fallback")
			}
		})
	}
}

func TestRunnerTerminalHTTP2MediaAndSizeFailures(t *testing.T) {
	var factories atomic.Int32
	mux := http.NewServeMux()
	RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return nil, errors.New("rejected input must not construct an engine")
	}))
	server := terminalProtocolServer(t, mux, 2)
	for _, control := range []struct {
		name, media, body string
		status            int
	}{
		{"missing-media", "", "{}", http.StatusUnsupportedMediaType},
		{"wrong-media", "text/plain", "{}", http.StatusUnsupportedMediaType},
		{"oversized", "application/json", strings.Repeat(" ", (4<<20)+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(control.name, func(t *testing.T) {
			response, err := server.Client().Post(server.URL+"/api/v1/runner/run", control.media, strings.NewReader(control.body))
			require.NoError(t, err)
			require.Equal(t, 2, response.ProtoMajor)
			require.Equal(t, control.status, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			envelope, err := runnerterminal.Validate(body, nil)
			require.NoError(t, err, "%s", body)
			require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
			require.Equal(t, "input", envelope.FailureBoundary)
			require.Nil(t, envelope.Identity)
			require.Nil(t, envelope.Observation)
			require.Zero(t, envelope.EngineCalls)
			require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
			require.Zero(t, factories.Load())
		})
	}
}
