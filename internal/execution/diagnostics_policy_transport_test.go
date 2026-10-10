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
	"github.com/stretchr/testify/require"
)

func TestDiagnosticNativeTransportCallbackPolicy(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, unsupported := range []bool{false, true} {
			t.Run(fmt.Sprintf("resume=%t/unsupported=%t", resume, unsupported), func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				done := make(chan struct{})
				var sends, callbacks atomic.Int32
				allowed := []string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"}
				go func() {
					defer close(done)
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer func() {
						if err := conn.Close(); err != nil {
							t.Errorf("closing offline policy connection: %v", err)
						}
					}()
					write := func(payload any) {
						if err := writeTestRPCFrame(conn, payload); err != nil {
							t.Errorf("writing offline policy frame: %v", err)
						}
					}
					reader := bufio.NewReader(conn)
					sessionID, turn := "", 0
					emit := func(data copilot.SessionEventData) {
						write(map[string]any{
							"jsonrpc": "2.0", "method": "session.event",
							"params": map[string]any{"sessionId": sessionID, "event": copilot.SessionEvent{Data: data}},
						})
					}
					denyPermission := func() {
						emit(&copilot.PermissionRequestedData{
							RequestID: "deny", PermissionRequest: &copilot.PermissionRequestShell{},
						})
					}
					for {
						body, err := readTestRPCFrame(reader)
						if err != nil {
							return
						}
						var req struct {
							ID     json.RawMessage `json:"id"`
							Method string          `json:"method"`
							Params struct {
								SessionID         string   `json:"sessionId"`
								AvailableTools    []string `json:"availableTools"`
								Hooks             bool     `json:"hooks"`
								RequestPermission bool     `json:"requestPermission"`
								RequestID         string   `json:"requestId"`
								Result            struct {
									Kind string `json:"kind"`
								} `json:"result"`
							} `json:"params"`
							Result struct {
								Output struct {
									PermissionDecision string `json:"permissionDecision"`
								} `json:"output"`
							} `json:"result"`
							Error json.RawMessage `json:"error"`
						}
						if err := json.Unmarshal(body, &req); err != nil {
							t.Errorf("decoding offline policy frame: %v", err)
							return
						}
						if req.Method == "" {
							require.Empty(t, req.Error)
							require.Equal(t, "deny", req.Result.Output.PermissionDecision)
							turn++
							if turn < 2 {
								denyPermission()
							} else {
								emit(&copilot.SessionIdleData{})
							}
							continue
						}
						response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
						switch req.Method {
						case "connect":
							response["result"] = map[string]any{"ok": true, "protocolVersion": 3, "version": "offline-policy"}
						case "auth.getStatus":
							response["result"] = map[string]any{"isAuthenticated": true}
						case "session.create", "session.resume":
							require.Equal(t, resume, req.Method == "session.resume")
							require.ElementsMatch(t, allowed, req.Params.AvailableTools)
							require.True(t, req.Params.Hooks)
							require.True(t, req.Params.RequestPermission)
							sessionID = req.Params.SessionID
							if unsupported {
								response["error"] = map[string]any{"code": -32000, "message": "offline policy unsupported"}
							} else {
								response["result"] = map[string]any{"sessionId": sessionID}
							}
						case "session.send":
							sends.Add(1)
							response["result"] = map[string]any{"messageId": "policy-message"}
						case "session.permissions.handlePendingPermissionRequest":
							if req.Params.RequestID == "deny" {
								require.Equal(t, "reject", req.Params.Result.Kind)
							} else {
								require.Equal(t, "approve-once", req.Params.Result.Kind)
							}
							response["result"] = map[string]any{}
						case "session.tools.handlePendingToolCall":
							response["result"] = map[string]any{}
						case "session.usage.getMetrics":
							response["result"] = completeDiagnosticMetrics()
						case "session.detach", "session.delete":
							response["result"] = map[string]any{"success": true}
						default:
							t.Errorf("unexpected offline policy method: %s", req.Method)
							return
						}
						write(response)
						switch req.Method {
						case "session.send":
							denyPermission()
						case "session.permissions.handlePendingPermissionRequest":
							if req.Params.RequestID == "deny" {
								emit(&copilot.PermissionRequestedData{
									RequestID: "allow",
									PermissionRequest: &copilot.PermissionRequestCustomTool{
										ToolName: []string{"set_waza_grade_pass", "set_waza_grade_fail"}[turn],
									},
								})
							} else {
								emit(&copilot.ExternalToolRequestedData{
									RequestID: fmt.Sprintf("callback-%d", turn), SessionID: sessionID,
									ToolCallID: fmt.Sprintf("tool-%d", turn),
									ToolName:   []string{"set_waza_grade_pass", "set_waza_grade_fail"}[turn],
									Arguments:  map[string]any{"reason": "offline callback"},
								})
							}
						case "session.tools.handlePendingToolCall":
							write(map[string]any{
								"jsonrpc": "2.0", "id": fmt.Sprintf("hook-%d", turn), "method": "hooks.invoke",
								"params": map[string]any{
									"sessionId": sessionID, "hookType": "preToolUse",
									"input": map[string]any{"toolName": "unknown"},
								},
							})
						}
					}
				}()
				t.Cleanup(func() {
					require.NoError(t, listener.Close())
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("offline policy transport did not stop")
					}
				})
				e := offlineDiagnosticEngine(t, listener.Addr().String(), func(ExecutionDiagnostic) error { return nil })
				req := &ExecutionRequest{
					Message: "judge", NoSkills: true, EphemeralSession: true, SkipWorkspaceCapture: true,
					ToolPolicy: NewToolPolicy(&allowed),
				}
				for _, name := range []string{"set_waza_grade_pass", "set_waza_grade_fail"} {
					req.Tools = append(req.Tools, copilot.Tool{
						Name: name, Handler: func(copilot.ToolInvocation) (copilot.ToolResult, error) {
							callbacks.Add(1)
							return copilot.ToolResult{TextResultForLLM: "offline callback collected"}, nil
						},
					})
				}
				if resume {
					req.SessionID = "offline-policy-resume"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				resp, err := e.Execute(ctx, req)
				if unsupported {
					require.Error(t, err)
					require.NotContains(t, err.Error(), "offline policy unsupported")
					require.Nil(t, resp)
					require.Zero(t, sends.Load())
					require.Zero(t, callbacks.Load())
				} else {
					require.NoError(t, err)
					require.False(t, resp.Success)
					require.Len(t, resp.ToolPolicyDenials, 4)
					require.EqualValues(t, 1, sends.Load())
					require.EqualValues(t, 2, callbacks.Load())
				}
				require.NoError(t, e.Shutdown(t.Context()))
			})
		}
	}
}
