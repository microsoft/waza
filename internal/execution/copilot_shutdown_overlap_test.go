package execution

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type observedDisconnectClient struct {
	CopilotClient
	disconnected chan struct{}
}

func (c *observedDisconnectClient) CreateSession(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
	session, err := c.CopilotClient.CreateSession(ctx, config)
	if err != nil {
		return nil, err
	}
	return &observedDisconnectSession{CopilotSession: session, disconnected: c.disconnected}, nil
}

type observedDisconnectSession struct {
	CopilotSession
	disconnected chan struct{}
}

func (s *observedDisconnectSession) Disconnect() error {
	defer close(s.disconnected)
	return s.CopilotSession.Disconnect()
}

func TestShutdownUsageDoesNotReenterAbandonedDisconnect(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			abandon   bool
			metrics   bool
			ephemeral bool
			early     bool
		}{
			{name: "abandoned without metrics", abandon: true},
			{name: "shutdown during disconnect", abandon: true, early: true},
			{name: "joined without metrics"},
			{name: "abandoned with metrics", abandon: true, metrics: true},
			{name: "ephemeral fallback before disconnect", abandon: true, ephemeral: true},
		} {
			if diagnostic && tc.early {
				continue // Diagnostic shutdown already waits for Execute to return.
			}
			t.Run(fmt.Sprintf("%s/diagnostic=%t", tc.name, diagnostic), func(t *testing.T) {
				resetSharedClientForTest()
				t.Cleanup(resetSharedClientForTest)
				oldConstruct, oldEmbedded, oldGrace := sharedConstruct, embeddedCLIPath, sendCancellationGrace
				t.Cleanup(func() {
					sharedConstruct, embeddedCLIPath, sendCancellationGrace = oldConstruct, oldEmbedded, oldGrace
				})
				sendCancellationGrace = 5 * time.Millisecond
				t.Setenv("COPILOT_CLI_PATH", "")
				t.Setenv("COPILOT_BASE_URL", "")
				t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
				embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }

				var log bytes.Buffer
				oldLog := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
				t.Cleanup(func() { slog.SetDefault(oldLog) })

				detachStarted := make(chan struct{})
				detachReleased := make(chan struct{})
				releaseDetach := make(chan struct{})
				var detachOnce, releaseOnce sync.Once
				server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
					switch req.Method {
					case "session.send":
						s.respond(req, map[string]any{"messageId": "message"}, false)
						s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.UserMessageData{
							Content: req.Params.Prompt, MessageID: utils.Ptr("message"),
						}})
						s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.AssistantUsageData{
							Model: "observed-model", InputTokens: utils.Ptr(int64(7)),
						}})
						s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
					case "session.usage.getMetrics":
						if tc.metrics {
							s.respond(req, completeDiagnosticMetrics(), false)
						} else {
							s.respond(req, nil, true)
						}
					case "session.detach":
						first := false
						detachOnce.Do(func() {
							first = true
							close(detachStarted)
						})
						if first {
							if tc.abandon {
								<-releaseDetach
							}
							close(detachReleased)
						}
						s.respond(req, map[string]any{"success": true}, false)
					case "session.shutdown":
						s.respond(req, map[string]any{}, false)
					case "session.getMessages":
						s.respond(req, map[string]any{"events": []copilot.SessionEvent{{Data: &copilot.SessionShutdownData{
							TotalNanoAiu: utils.Ptr(2e9), TotalPremiumRequests: utils.Ptr(1.0),
							ModelMetrics: map[string]copilot.ShutdownModelMetric{
								"observed-model": {
									TotalNanoAiu: utils.Ptr(2e9),
									Requests: rpc.ShutdownModelMetricRequests{
										Count: utils.Ptr(int64(1)), Cost: utils.Ptr(1.0),
									},
								},
							},
						}}}}, false)
					default:
						s.common(req)
					}
				})
				disconnected := make(chan struct{})
				sharedConstruct = func(options *copilot.ClientOptions) CopilotClient {
					options.Connection = copilot.URIConnection{URL: address}
					return &observedDisconnectClient{CopilotClient: newCopilotClient(options), disconnected: disconnected}
				}
				var seen []ExecutionDiagnostic
				var seenMu sync.Mutex
				opts := &CopilotEngineBuilderOptions{}
				if diagnostic {
					opts.DiagnosticObserver = func(d ExecutionDiagnostic) error {
						seenMu.Lock()
						defer seenMu.Unlock()
						seen = append(seen, d)
						return nil
					}
				}
				engine := NewCopilotEngineBuilder("test-model", opts).Build()
				require.False(t, engine.ownsClient, "exercise production shared-client ownership")
				t.Cleanup(func() {
					releaseOnce.Do(func() { close(releaseDetach) })
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					err := engine.Shutdown(ctx)
					if !diagnostic {
						require.NoError(t, err)
					}
					require.NoError(t, ShutdownSharedClient(ctx))
				})
				require.NoError(t, engine.Initialize(t.Context()))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				type result struct {
					response *ExecutionResponse
					err      error
				}
				executed := make(chan result, 1)
				executeDone := make(chan struct{})
				go func() {
					defer close(executeDone)
					response, err := engine.Execute(ctx, &ExecutionRequest{
						Message: "offline usage", NoSkills: true, EphemeralSession: tc.ephemeral,
						SkipWorkspaceCapture: true,
					})
					executed <- result{response, err}
				}()
				t.Cleanup(func() {
					cancel()
					releaseOnce.Do(func() { close(releaseDetach) })
					awaitStartup(t, executeDone)
					awaitStartup(t, disconnected)
				})
				awaitStartup(t, detachStarted)
				shutdownCtx, stopShutdown := context.WithTimeout(t.Context(), time.Second)
				defer stopShutdown()
				var shutdownErr error
				if tc.early {
					shutdownErr = engine.Shutdown(shutdownCtx)
				}
				if tc.abandon {
					cancel()
				}
				var execution result
				select {
				case execution = <-executed:
				case <-time.After(5 * time.Second):
					t.Fatal("Execute did not finish within bounded disconnect grace")
				}
				require.NotNil(t, execution.response)
				if !diagnostic {
					require.NoError(t, execution.err)
				}
				id := execution.response.SessionID
				require.NotEmpty(t, id)
				require.Equal(t, 1, server.count("session.usage.getMetrics", id))
				if tc.abandon {
					require.False(t, execution.response.Success)
					select {
					case <-detachReleased:
						t.Fatal("fixture did not keep disconnect unfinished")
					default:
					}
				}
				if !tc.early {
					shutdownErr = engine.Shutdown(shutdownCtx)
				}
				skipped := tc.abandon && !tc.metrics && !tc.ephemeral
				if skipped {
					require.Zero(t, server.count("session.resume", id), "shutdown must not resume a session while its disconnect is unfinished")
					require.Zero(t, server.count("session.shutdown", id), "shutdown must not reenter the abandoned session")
					require.Nil(t, engine.SessionUsage(id).AICredits, "missing final credits must not become invented totals")
					require.Equal(t, 7, engine.SessionUsage(id).InputTokens, "retain observed per-turn usage")
					if diagnostic {
						require.ErrorContains(t, shutdownErr, string(CodeUsageMissingOrPartial))
						seenMu.Lock()
						require.Contains(t, seen, ExecutionDiagnostic{Stage: StageShutdownUsage, Code: CodeUsageMissingOrPartial, SessionID: id})
						seenMu.Unlock()
					} else {
						require.NoError(t, shutdownErr)
						require.Contains(t, log.String(), "shutdown usage unavailable: disconnect not joined")
					}
				} else {
					require.NoError(t, shutdownErr)
					if tc.metrics {
						require.Zero(t, server.count("session.shutdown", id))
						require.Equal(t, 0.0, *engine.SessionUsage(id).AICredits, "preserve authoritative recorded zero")
					} else {
						require.Equal(t, 1, server.count("session.shutdown", id))
						require.Equal(t, 2.0, *engine.SessionUsage(id).AICredits)
					}
				}
				if tc.ephemeral && tc.abandon {
					require.Zero(t, server.count("session.delete", id), "abandoned ephemeral cleanup remains deferred")
				} else {
					require.Equal(t, 1, server.count("session.delete", id))
				}
				require.NoError(t, shutdownCtx.Err(), "shutdown must not wait for the unfinished disconnect")
			})
		}
	}
}
