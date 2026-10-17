package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestEvidenceJudgeSDKWireAndBoundedDisconnect(t *testing.T) {
	evidenceJudgeTestProvider(t)
	const credential = "SYNTHETIC_JUDGE_WIRE_CREDENTIAL_CANARY"
	const providerPath = "/untrusted/provider/wire-error-path-canary"
	t.Setenv("COPILOT_API_KEY", credential)
	privateFailure := fmt.Sprintf("SDK payload %s; credential %s; path %s",
		evidenceJudgeTestRequest().Message, credential, providerPath)
	for _, test := range []struct {
		name          string
		unsupported   bool
		patchError    bool
		stallDetach   bool
		hiddenTool    bool
		wrongIdentity bool
		sendError     bool
	}{
		{name: "wire_and_callback"},
		{name: "unsupported_controls", unsupported: true},
		{name: "unsupported_mode_patch", unsupported: true, patchError: true},
		{name: "bounded_failed_mode_patch_cleanup", unsupported: true, patchError: true, stallDetach: true},
		{name: "bounded_disconnect_no_restart", stallDetach: true},
		{name: "persisted_post_grade_violation", hiddenTool: true},
		{name: "unexpected_session_identity", wrongIdentity: true},
		{name: "send_error_payload_redacted", sendError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			done := make(chan struct{})
			var creates, sends, callbacks, deletes atomic.Int32
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() {
					require.NoError(t, conn.Close())
				}()
				reader := bufio.NewReader(conn)
				write := func(value any) bool {
					return writeTestRPCFrame(conn, value) == nil
				}
				id := ""
				emit := func(data copilot.SessionEventData) bool {
					return write(map[string]any{
						"jsonrpc": "2.0", "method": "session.event",
						"params": map[string]any{"sessionId": id, "event": copilot.SessionEvent{Data: data}},
					})
				}
				for {
					body, err := readTestRPCFrame(reader)
					if err != nil {
						return
					}
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string
						Params map[string]json.RawMessage
					}
					require.NoError(t, json.Unmarshal(body, &request))
					response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
					switch request.Method {
					case "connect":
						response["result"] = map[string]any{"ok": true, "protocolVersion": 3, "version": "offline-wire"}
					case "session.create":
						creates.Add(1)
						require.NoError(t, json.Unmarshal(request.Params["sessionId"], &id))
						require.NotEmpty(t, id)
						for key, want := range map[string]bool{
							"enableConfigDiscovery": false, "skipEmbeddingRetrieval": true,
							"enableOnDemandInstructionDiscovery": false, "enableFileHooks": false,
							"enableHostGitOperations": false, "enableSessionStore": false,
							"enableSkills": false, "skipCustomInstructions": true,
							"isExperimentalMode": false, "enableSessionTelemetry": false,
							"enableFileChangeTracking": false, "coauthorEnabled": false, "manageScheduleEnabled": false,
						} {
							actual, present := request.Params[key]
							require.True(t, present, "wire must explicitly carry %s", key)
							require.JSONEq(t, fmt.Sprint(want), string(actual), key)
						}
						var filter []string
						require.NoError(t, json.Unmarshal(request.Params["availableTools"], &filter))
						require.ElementsMatch(t, evidenceJudgeToolNames, filter)
						var system copilot.SystemMessageConfig
						require.NoError(t, json.Unmarshal(request.Params["systemMessage"], &system))
						require.Equal(t, "replace", system.Mode)
						require.Equal(t, evidenceJudgeSystemMessage, system.Content)
						for _, field := range []string{"mcpServers", "customAgents", "skillDirectories", "pluginDirectories", "instructionDirectories"} {
							require.NotContains(t, request.Params, field)
						}
						if test.unsupported && !test.patchError {
							response["error"] = map[string]any{"code": -32602, "message": privateFailure}
						} else {
							actualID := id
							if test.wrongIdentity {
								actualID = "not-the-owned-session"
							}
							response["result"] = map[string]any{"sessionId": actualID}
						}
					case "session.send":
						sends.Add(1)
						if test.sendError {
							response["error"] = map[string]any{"code": -32603, "message": privateFailure}
						} else {
							response["result"] = map[string]any{"messageId": "judge-message"}
						}
					case "session.options.update":
						for key, want := range map[string]bool{
							"skipCustomInstructions": true, "customAgentsLocalOnly": true,
							"coauthorEnabled": false, "manageScheduleEnabled": false,
						} {
							require.JSONEq(t, fmt.Sprint(want), string(request.Params[key]), key)
						}
						require.JSONEq(t, "[]", string(request.Params["installedPlugins"]))
						require.JSONEq(t, "[]", string(request.Params["includedBuiltinSkills"]))
						if test.patchError {
							response["error"] = map[string]any{"code": -32602, "message": privateFailure}
						} else {
							response["result"] = map[string]any{}
						}
					case "session.permissions.handlePendingPermissionRequest":
						var decision struct{ Kind string }
						require.NoError(t, json.Unmarshal(request.Params["result"], &decision))
						require.Equal(t, "approve-once", decision.Kind)
						response["result"] = map[string]any{}
					case "session.tools.handlePendingToolCall":
						response["result"] = map[string]any{}
					case "session.shutdown":
						response["result"] = map[string]any{}
					case "session.getMessages":
						history := []copilot.SessionEvent{
							{Data: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(2e9)}},
						}
						if test.hiddenTool {
							history = append(history, copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolName: "bash", ToolCallID: "post-grade"}})
						}
						response["result"] = map[string]any{"events": history}
					case "session.usage.getMetrics":
						response["result"] = &rpc.UsageGetMetricsResult{
							TotalNanoAiu: utils.Ptr(0.0), ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
								"wire-model": {TotalNanoAiu: utils.Ptr(0.0), Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 3}},
							},
						}
					case "session.detach":
						if test.stallDetach {
							// The owned SDK transport must be force-closed;
							// no fake runtime or worker is left behind.
							continue
						}
						response["result"] = map[string]any{"success": true}
					case "session.delete":
						deletes.Add(1)
						response["result"] = map[string]any{"success": true}
					default:
						t.Errorf("unexpected judge wire method %q", request.Method)
						return
					}
					if !write(response) {
						return
					}
					switch request.Method {
					case "session.send":
						if !test.sendError {
							emit(&copilot.PermissionRequestedData{
								RequestID: "permission", PermissionRequest: &copilot.PermissionRequestCustomTool{
									ToolName: "set_waza_grade_pass", ToolCallID: utils.Ptr("grade"),
								},
							})
						}
					case "session.permissions.handlePendingPermissionRequest":
						emit(&copilot.ExternalToolRequestedData{
							RequestID: "callback", SessionID: id, ToolCallID: "grade", ToolName: "set_waza_grade_pass",
							Arguments: map[string]any{},
						})
					case "session.tools.handlePendingToolCall":
						emit(&copilot.ToolExecutionStartData{ToolName: "set_waza_grade_pass", ToolCallID: "grade"})
						emit(&copilot.ToolExecutionCompleteData{ToolCallID: "grade", Success: true})
						emit(&copilot.SessionIdleData{})
					}
				}
			}()
			t.Cleanup(func() {
				require.NoError(t, listener.Close())
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("judge wire transport did not stop")
				}
			})
			executor := NewEvidenceJudgeExecutor()
			executor.timeout = 50 * time.Millisecond
			executor.resolveCLI = func(context.Context, *evidenceJudgeDirs) (string, error) { return "/never/launched", nil }
			executor.newClient = func(options *copilot.ClientOptions) evidenceJudgeClient {
				options.Connection = copilot.URIConnection{URL: listener.Addr().String()}
				options.UseLoggedInUser, options.GitHubToken = nil, ""
				return newEvidenceJudgeClient(options)
			}
			req := evidenceJudgeTestRequest()
			req.Tools[0].Handler = func(copilot.ToolInvocation) (copilot.ToolResult, error) {
				callbacks.Add(1)
				return copilot.ToolResult{ResultType: "success", TextResultForLLM: "grade collected"}, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response, err := executor.Execute(ctx, req)
			require.EqualValues(t, 1, creates.Load())
			if test.unsupported || test.sendError {
				require.Error(t, err)
				require.False(t, response.Success)
				require.Equal(t, err.Error(), response.ErrorMsg)
				for _, canary := range []string{evidenceJudgeTestRequest().Message, credential, providerPath, "SDK payload"} {
					require.NotContains(t, response.ErrorMsg, canary)
				}
				var protected *evidenceJudgeError
				require.ErrorAs(t, err, &protected)
				if !test.stallDetach {
					require.Contains(t, protected.Unwrap().Error(), privateFailure, "explicit unwrapping retains the actual SDK cause")
				}
			}
			if test.unsupported || test.wrongIdentity {
				require.Error(t, err)
				require.False(t, response.Success)
				require.Zero(t, sends.Load())
				require.Zero(t, callbacks.Load())
				require.Empty(t, response.SessionID, "never bind an unknown session as judge evidence")
				return
			}
			if test.sendError {
				require.EqualValues(t, 1, sends.Load())
				require.Zero(t, callbacks.Load())
				require.EqualValues(t, 1, deletes.Load())
				return
			}
			require.EqualValues(t, 1, callbacks.Load())
			require.EqualValues(t, 1, sends.Load())
			require.NotNil(t, response.Usage.AICredits)
			require.Zero(t, *response.Usage.AICredits)
			if test.stallDetach {
				require.Error(t, err)
				require.False(t, response.Success)
				require.Zero(t, deletes.Load(), "do not restart a stopped runtime for deletion")
			} else if test.hiddenTool {
				require.Error(t, err)
				require.False(t, response.Success)
				require.NotEmpty(t, response.ToolPolicyDenials, "persisted history must expose a violation not delivered live")
			} else {
				require.NoError(t, err)
				require.True(t, response.Success)
				require.EqualValues(t, 1, deletes.Load())
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Execute returned with a transport worker still alive")
			}
		})
	}
}
