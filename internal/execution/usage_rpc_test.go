package execution

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func usageRPCSession(t *testing.T, failMethod string, events []copilot.SessionEvent) (CopilotSession, func() []string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var methods []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("closing test RPC connection: %v", err)
			}
		}()
		reader := bufio.NewReader(conn)
		for {
			body, err := readTestRPCFrame(reader)
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
				} `json:"params"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
				return
			}
			if len(req.ID) == 0 {
				continue
			}
			mu.Lock()
			methods = append(methods, req.Method)
			mu.Unlock()
			if strings.HasPrefix(req.Method, "session.") && req.Method != "session.create" && req.Params.SessionID != "usage-session" {
				t.Errorf("wrong session ID in %s: %q", req.Method, req.Params.SessionID)
			}
			response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
			if req.Method == failMethod {
				response["error"] = map[string]any{"code": -32000, "message": "test RPC failure"}
			} else {
				switch req.Method {
				case "connect":
					response["result"] = map[string]any{"ok": true, "protocolVersion": 3, "version": "test"}
				case "session.create", "session.resume":
					response["result"] = map[string]any{"sessionId": "usage-session"}
				case "session.usage.getMetrics":
					response["result"] = &rpc.UsageGetMetricsResult{
						TotalNanoAiu: utils.Ptr(1_123_456_789.0),
						ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
							"model": {TotalNanoAiu: utils.Ptr(1_123_456_789.0), Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 7}},
						},
					}
				case "session.getMessages":
					response["result"] = map[string]any{"events": events}
				case "session.shutdown":
					response["result"] = map[string]any{}
				case "session.detach":
					response["result"] = map[string]any{"success": true}
				default:
					t.Errorf("unexpected RPC %s", req.Method)
					response["error"] = map[string]any{"code": -32601, "message": "unexpected method"}
				}
			}
			if err := writeTestRPCFrame(conn, response); err != nil {
				return
			}
		}
	}()
	client := copilot.NewClient(&copilot.ClientOptions{Connection: copilot.URIConnection{URL: listener.Addr().String()}})
	t.Cleanup(func() {
		err := client.Stop()
		if failMethod == "session.detach" {
			require.ErrorContains(t, err, "test RPC failure")
		} else {
			require.NoError(t, err)
		}
		require.NoError(t, listener.Close())
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("RPC server did not stop")
		}
	})
	require.NoError(t, client.Start(t.Context()))
	session, err := (&copilotClientWrapper{inner: client}).CreateSession(t.Context(), &copilot.SessionConfig{SessionID: "usage-session"})
	require.NoError(t, err)
	return session, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), methods...)
	}
}

func TestUsageRPCWrapperUsesPinnedSDKAPI(t *testing.T) {
	session, _ := usageRPCSession(t, "", nil)
	metrics, err := session.UsageMetrics(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1_123_456_789.0, *metrics.TotalNanoAiu)
	require.Equal(t, int64(7), metrics.ModelMetrics["model"].Usage.InputTokens)
	require.NoError(t, session.Disconnect())
}

func TestUsageRPCWrapperFallbackAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failMethod string
		detached   bool
		noEvent    bool
	}{
		{"attached shutdown", "", false, false},
		{"detached resume shutdown", "", true, false},
		{"metrics error", "session.usage.getMetrics", false, false},
		{"resume error", "session.resume", true, false},
		{"shutdown error", "session.shutdown", false, false},
		{"history error", "session.getMessages", false, false},
		{"missing shutdown", "", false, true},
		{"disconnect error", "session.detach", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []copilot.SessionEvent{
				{Data: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(1e9)}},
				{Data: &copilot.UserMessageData{Content: "later turn"}},
				{Data: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(2e9)}},
			}
			if tc.noEvent {
				events = nil
			}
			session, methods := usageRPCSession(t, tc.failMethod, events)
			if tc.failMethod == "session.usage.getMetrics" {
				_, err := session.UsageMetrics(t.Context())
				require.ErrorContains(t, err, "test RPC failure")
				return
			}
			if tc.detached {
				err := session.Disconnect()
				if tc.failMethod == "session.detach" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}

			usage, err := session.ShutdownUsage(t.Context())
			if tc.noEvent {
				require.ErrorContains(t, err, "no final session.shutdown")
			} else if tc.failMethod != "" && tc.failMethod != "session.detach" {
				require.ErrorContains(t, err, "test RPC failure")
			} else {
				require.NoError(t, err)
				require.Equal(t, 2e9, *usage.TotalNanoAiu)
			}
			if tc.detached && tc.failMethod != "session.resume" {
				require.Contains(t, methods(), "session.resume")
			}
		})
	}
}
