package responder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type loopbackTransport struct {
	origin string
	client *http.Transport
}

func (t *loopbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return nil, fmt.Errorf("responder probe denied non-loopback inference")
	}
	return t.client.RoundTrip(req)
}

type terminalControlExecutor struct {
	Executor
	nonterminal bool
	callbacks   atomic.Int32
	sessionIDs  []string
}

func (e *terminalControlExecutor) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.sessionIDs = append(e.sessionIDs, req.SessionID)
	allowed := []string{"custom:" + toolRespond, "custom:" + toolStop, "custom:" + toolAbstain}
	req.ToolPolicy = execution.NewToolPolicy(&allowed)
	for i := range req.Tools {
		if e.nonterminal {
			req.Tools[i].IsTerminal = false
		}
		handler := req.Tools[i].Handler
		req.Tools[i].Handler = func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
			e.callbacks.Add(1)
			return handler(inv)
		}
	}
	return e.Executor.Execute(ctx, req)
}

func responderRuntimeCLI(t *testing.T) string {
	t.Helper()
	if os.Getenv("WAZA_RESPONDER_RUNTIME_TEST") != "1" {
		t.Skip("set WAZA_RESPONDER_RUNTIME_TEST=1 and COPILOT_CLI_PATH for the loopback runtime probe")
	}
	cli := os.Getenv("COPILOT_CLI_PATH")
	require.True(t, filepath.IsAbs(cli), "probe requires an explicit absolute native CLI path")
	info, err := os.Stat(cli)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	t.Chdir(t.TempDir())
	for _, name := range []string{
		"COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER", "COPILOT_PROVIDER_TYPE",
		"COPILOT_WIRE_API", "COPILOT_PROVIDER_WIRE_API", "COPILOT_API_KEY", "COPILOT_PROVIDER_API_KEY",
		"COPILOT_BEARER_TOKEN", "COPILOT_PROVIDER_BEARER_TOKEN",
	} {
		t.Setenv(name, "")
	}
	return cli
}

func newResponderRuntimeEngine(t *testing.T, cli, origin string, emptyMode bool) (*execution.CopilotEngine, context.Context) {
	t.Helper()
	t.Setenv("COPILOT_BASE_URL", origin+"/v1")
	t.Setenv("COPILOT_API_KEY", "synthetic-responder-key")
	t.Setenv("COPILOT_PROVIDER_TYPE", "openai")
	t.Setenv("COPILOT_WIRE_API", "completions")
	home := t.TempDir()
	transport := &loopbackTransport{origin: origin, client: &http.Transport{Proxy: nil}}
	t.Cleanup(transport.client.CloseIdleConnections)
	engine := execution.NewCopilotEngineBuilder("gpt-4.1", &execution.CopilotEngineBuilderOptions{
		DisableCommandMocks: true,
		NewCopilotClient: func(options *copilot.ClientOptions) execution.CopilotClient {
			env := []string{"HOME=" + home, "USERPROFILE=" + home, "COPILOT_HOME=" + home}
			for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "PATH"} {
				if value, ok := os.LookupEnv(key); ok {
					env = append(env, key+"="+value)
				}
			}
			no := false
			options.Connection = copilot.StdioConnection{Path: cli, Env: env}
			options.WorkingDirectory, options.BaseDirectory = home, home
			options.UseLoggedInUser = &no
			if emptyMode {
				options.Mode = copilot.ModeEmpty
			}
			options.RequestHandler = &copilot.CopilotRequestHandler{
				Transport: transport,
				OpenWebSocket: func(*copilot.CopilotRequestContext) (copilot.CopilotWebSocketHandler, error) {
					return nil, fmt.Errorf("responder probe denies WebSockets")
				},
			}
			return execution.NewOwnedCopilotClient(options)
		},
	}).Build()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, engine.Initialize(ctx))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, engine.Shutdown(cleanup))
	})
	return engine, ctx
}

// These opt-in probes use the real SDK and native CLI, but only synthetic
// responses from a loopback provider. They never call a paid model.
func TestResponderTerminalRuntime(t *testing.T) {
	cli := responderRuntimeCLI(t)
	for _, tc := range []struct {
		name string
		args string
		want Decision
	}{
		{toolRespond, `{"answer":"synthetic answer"}`, Decision{Kind: DecisionReply, Answer: "synthetic answer"}},
		{toolStop, `{}`, Decision{Kind: DecisionStop}},
		{toolAbstain, `{"reason":"synthetic missing fact"}`, Decision{Kind: DecisionAbstain, Reason: "synthetic missing fact"}},
	} {
		for _, mode := range []string{"terminal", "nonterminal", "terminal_duplicate", "nonterminal_duplicate", "conflict"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				nonterminal := mode == "nonterminal" || mode == "nonterminal_duplicate"
				duplicateNextRound := mode == "terminal_duplicate" || mode == "nonterminal_duplicate"
				conflictTool := toolStop
				conflictArgs := `{}`
				if tc.name == toolStop {
					conflictTool, conflictArgs = toolRespond, `{"answer":"conflicting answer"}`
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := io.Copy(io.Discard, io.LimitReader(r.Body, 4<<20))
					if err != nil {
						t.Errorf("reading synthetic request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					count := requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					message := map[string]any{"role": "assistant", "content": "unnecessary continuation"}
					finish := "stop"
					if count == 1 {
						message["content"] = nil
						calls := []any{map[string]any{
							"id": "decision", "type": "function",
							"function": map[string]any{"name": tc.name, "arguments": tc.args},
						}}
						if mode == "conflict" {
							calls = append(calls, map[string]any{
								"id": "conflicting-decision", "type": "function",
								"function": map[string]any{"name": conflictTool, "arguments": conflictArgs},
							})
						}
						message["tool_calls"] = calls
						finish = "tool_calls"
					} else if count == 2 && duplicateNextRound {
						message["content"] = nil
						message["tool_calls"] = []any{map[string]any{
							"id": "later-decision", "type": "function",
							"function": map[string]any{"name": conflictTool, "arguments": conflictArgs},
						}}
						finish = "tool_calls"
					}
					err = json.NewEncoder(w).Encode(map[string]any{
						"id": "synthetic", "object": "chat.completion", "model": "gpt-4.1",
						"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
						"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
					})
					if err != nil {
						t.Errorf("writing synthetic response: %v", err)
					}
				}))
				defer server.Close()
				engine, ctx := newResponderRuntimeEngine(t, cli, server.URL, true)
				control := &terminalControlExecutor{Executor: engine, nonterminal: nonterminal}
				classifier := New(control, models.ResponderConfig{Instructions: "Use the synthetic decision."}, "gpt-4.1")
				var sessionID string
				for turn, phase := range []string{"initial", "resumed"} {
					requests.Store(0)
					control.callbacks.Store(0)
					decision, err := classifier.Classify(ctx, fmt.Sprintf("Synthetic agent message %d.", turn+1))
					t.Logf("%s: model requests=%d, decision callbacks=%d, classification error=%t",
						phase, requests.Load(), control.callbacks.Load(), err != nil)
					require.NotEmpty(t, classifier.sessionID)
					if turn == 0 {
						require.Empty(t, control.sessionIDs[turn])
						sessionID = classifier.sessionID
					} else {
						require.Equal(t, sessionID, control.sessionIDs[turn], "must exercise native session resumption")
						require.Equal(t, sessionID, classifier.sessionID)
					}

					if mode == "conflict" || mode == "nonterminal_duplicate" {
						require.ErrorContains(t, err, "responder tool call invalid: responder called ")
						require.ErrorContains(t, err, "after a decision was already recorded")
						require.Equal(t, Decision{}, decision)
						require.EqualValues(t, 2, control.callbacks.Load(), "both conflicting calls must reach the real recorder")
						if mode == "nonterminal_duplicate" {
							require.EqualValues(t, 3, requests.Load(), "ordinary tools allow the later decision and final recovery generation")
						}
					} else {
						require.NoError(t, err)
						require.Equal(t, tc.want, decision)
						require.EqualValues(t, 1, control.callbacks.Load())
						wantRequests := int32(1)
						if nonterminal {
							wantRequests = 2
						}
						require.Equal(t, wantRequests, requests.Load(), "successful terminal decision must prevent the next model request")
					}
				}
				require.Len(t, control.sessionIDs, 2)
			})
		}
	}
}

func TestResponderExecutionErrorRuntime(t *testing.T) {
	cli := responderRuntimeCLI(t)
	for _, outcome := range []string{"decision", "text", "provider failure"} {
		for _, phase := range []string{"initial", "resumed"} {
			t.Run(outcome+"/"+phase, func(t *testing.T) {
				var requests, callbacks atomic.Int32
				var finalTurn atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := io.Copy(io.Discard, io.LimitReader(r.Body, 4<<20))
					if err != nil {
						t.Errorf("reading synthetic request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if finalTurn.Load() && outcome == "provider failure" {
						w.WriteHeader(http.StatusBadRequest)
						_, err = io.WriteString(w, `{"error":{"message":"synthetic provider rejection","type":"invalid_request_error","code":"synthetic_rejection"}}`)
					} else {
						message := map[string]any{"role": "assistant", "content": "synthetic plain text"}
						finish := "stop"
						if !finalTurn.Load() || outcome == "decision" {
							message["content"] = nil
							message["tool_calls"] = []any{map[string]any{
								"id": "decision", "type": "function",
								"function": map[string]any{"name": toolRespond, "arguments": `{"answer":"synthetic answer"}`},
							}}
							finish = "tool_calls"
						}
						err = json.NewEncoder(w).Encode(map[string]any{
							"id": "synthetic", "object": "chat.completion", "model": "gpt-4.1",
							"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
							"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
						})
					}
					if err != nil {
						t.Errorf("writing synthetic response: %v", err)
					}
				}))
				defer server.Close()
				// Keep the default CLI mode and Classify's tool policy, unlike the
				// terminality probe's empty mode and explicit decision allowlist.
				engine, ctx := newResponderRuntimeEngine(t, cli, server.URL, false)
				var response *execution.ExecutionResponse
				exec := &fakeExecutor{respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
					require.Nil(t, req.ToolPolicy)
					for i := range req.Tools {
						require.True(t, req.Tools[i].IsTerminal)
						handler := req.Tools[i].Handler
						req.Tools[i].Handler = func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
							callbacks.Add(1)
							return handler(inv)
						}
					}
					var err error
					response, err = engine.Execute(ctx, req)
					require.NoError(t, err, "exercise the response-level failure contract, not a Go error")
					require.NotNil(t, response)
					return response, err
				}}
				classifier := New(exec, models.ResponderConfig{Instructions: "Use the synthetic decision."}, "gpt-4.1")
				turns := 1
				if phase == "resumed" {
					turns = 4
				}
				var sessionID string
				for turn := range turns {
					finalTurn.Store(turn == turns-1)
					requests.Store(0)
					callbacks.Store(0)
					d, err := classifier.Classify(ctx, "Synthetic question?")
					require.NotEmpty(t, response.SessionID)
					if turn == 0 {
						require.Empty(t, exec.calls[turn].SessionID)
						sessionID = response.SessionID
					} else {
						require.Equal(t, sessionID, exec.calls[turn].SessionID)
						require.Equal(t, sessionID, response.SessionID)
					}
					require.EqualValues(t, 1, requests.Load())
					if !finalTurn.Load() || outcome == "decision" {
						require.NoError(t, err)
						require.True(t, response.Success)
						require.Empty(t, response.ErrorMsg)
						require.Equal(t, Decision{Kind: DecisionReply, Answer: "synthetic answer"}, d)
						require.EqualValues(t, 1, callbacks.Load())
					} else {
						require.Equal(t, Decision{}, d)
						require.Zero(t, callbacks.Load())
						if outcome == "text" {
							require.True(t, response.Success)
							require.Empty(t, response.ErrorMsg)
							require.Equal(t, "synthetic plain text", response.FinalOutput)
							require.EqualError(t, err, "responder did not call a decision tool")
						} else {
							require.False(t, response.Success)
							require.Contains(t, response.ErrorMsg, "synthetic provider rejection")
							require.EqualError(t, err, "responder execution failed: "+response.ErrorMsg)
						}
					}
				}
				require.Len(t, exec.calls, turns)
			})
		}
	}
}
