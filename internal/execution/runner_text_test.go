package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

type runnerTextTransport struct {
	CopilotClient
	interrupted bool
}

func TestRunnerTextUsageFallbackTransport(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			for _, diagnostic := range []bool{false, true} {
				t.Run(fmt.Sprintf("restricted=%t/missing=%t/diagnostic=%t", restricted, missing, diagnostic), func(t *testing.T) {
					server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
						switch req.Method {
						case "session.options.update":
							s.respond(req, map[string]any{}, false)
						case "session.send":
							s.respond(req, map[string]any{"messageId": "offline-message"}, false)
							messageID := "offline-message"
							s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.UserMessageData{
								Content: req.Params.Prompt, MessageID: &messageID,
							}})
							s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "hello"}})
							s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
						case "session.usage.getMetrics":
							if missing {
								s.respond(req, nil, true)
							} else {
								s.common(req)
							}
						case "session.shutdown":
							s.respond(req, map[string]any{}, false)
						case "session.getMessages":
							s.respond(req, map[string]any{"events": []copilot.SessionEvent{{Data: &copilot.SessionShutdownData{
								TotalNanoAiu: new(0.0), TotalPremiumRequests: new(0.0),
								ModelMetrics: map[string]copilot.ShutdownModelMetric{"observed-model": {
									TotalNanoAiu: new(0.0),
									Requests:     copilot.ShutdownModelMetricRequests{Count: new(int64), Cost: new(0.0)},
								}},
							}}}}, false)
						default:
							s.common(req)
						}
					})
					var observer func(ExecutionDiagnostic) error
					if diagnostic {
						observer = func(ExecutionDiagnostic) error { return nil }
					}
					engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{
						NewCopilotClient: func(options *copilot.ClientOptions) CopilotClient {
							options.Connection = copilot.URIConnection{URL: address}
							return NewOwnedCopilotClient(options)
						},
						DisableCommandMocks: true, DisableAmbientDiscovery: restricted,
						DiagnosticObserver: observer,
					}).Build()
					engine.provider = customProviderConfig{}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					t.Cleanup(func() { engine.ForceStopRunnerText() })
					require.NoError(t, engine.Initialize(ctx))
					response, err := engine.Execute(ctx, &ExecutionRequest{
						Message: "hello", NoSkills: true, WorkspaceDir: t.TempDir(),
					})
					require.NotNil(t, response)
					require.Equal(t, "hello", response.FinalOutput)
					if missing && diagnostic {
						require.ErrorContains(t, err, "usage_missing_or_partial")
					} else {
						require.NoError(t, err)
					}
					shutdownErr := engine.Shutdown(ctx)
					if restricted && missing {
						if diagnostic {
							require.ErrorContains(t, shutdownErr, "shutdown_usage/usage_missing_or_partial")
						} else {
							require.NoError(t, shutdownErr)
						}
						require.Nil(t, engine.SessionUsage(response.SessionID), "unavailable billing must not become recorded zero")
						require.False(t, engine.SessionUsageObservation(response.SessionID).Complete)
					} else {
						require.NoError(t, shutdownErr)
						require.NotNil(t, engine.SessionUsage(response.SessionID))
						require.Equal(t, 0.0, *engine.SessionUsage(response.SessionID).AICredits)
						require.True(t, engine.SessionUsageObservation(response.SessionID).Complete)
					}
					expectedResume := 0
					if missing && !restricted {
						expectedResume = 1
					}
					require.Equal(t, expectedResume, server.count("session.resume", ""))
					require.Equal(t, expectedResume, server.count("session.shutdown", ""))
					require.Equal(t, expectedResume, server.count("session.getMessages", ""))
					require.Equal(t, 1, server.count("session.create", ""))
					require.Equal(t, 1, server.count("session.send", ""))
					require.Equal(t, 1, server.count("session.delete", ""))
				})
			}
		}
	}
}

func TestRunnerTextDiscoveryTransport(t *testing.T) {
	for _, control := range []string{"ordinary", "restricted", "update-failure"} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resume=%t", control, resume), func(t *testing.T) {
				disabled := control != "ordinary"
				captured := make(chan diagnosticRPCRequest, 2)
				server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
					switch req.Method {
					case "session.create", "session.resume":
						captured <- req
						s.common(req)
					case "session.options.update":
						captured <- req
						s.respond(req, map[string]any{}, control == "update-failure")
					case "session.send":
						s.respond(req, nil, true)
					default:
						s.common(req)
					}
				})
				engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{
					NewCopilotClient: func(options *copilot.ClientOptions) CopilotClient {
						options.Connection = copilot.URIConnection{URL: address}
						return NewOwnedCopilotClient(options)
					},
					DisableCommandMocks:     true,
					DisableAmbientDiscovery: disabled,
					DiagnosticObserver:      func(ExecutionDiagnostic) error { return nil },
				}).Build()
				engine.provider = customProviderConfig{}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					require.NoError(t, engine.Shutdown(ctx))
				})
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				require.NoError(t, engine.Initialize(ctx))
				sessionID := ""
				if resume {
					sessionID = "existing"
				}
				response, err := engine.Execute(ctx, &ExecutionRequest{
					SessionID: sessionID, Message: "hello", NoSkills: true, WorkspaceDir: t.TempDir(),
				})
				require.Error(t, err, "the offline peer rejects options update or send after capturing wire fields")
				if control == "update-failure" {
					code := "execute/create_failed"
					if resume {
						code = "execute/resume_failed"
					}
					require.ErrorContains(t, err, code)
					require.Nil(t, response)
					require.Zero(t, server.count("session.send", ""))
				} else {
					require.Equal(t, 1, server.count("session.send", ""))
				}
				var request diagnosticRPCRequest
				select {
				case request = <-captured:
				case <-ctx.Done():
					t.Fatal("SDK did not emit the session request")
				}
				method := "session.create"
				if resume {
					method = "session.resume"
				}
				require.Equal(t, method, request.Method)
				require.Equal(t, 1, server.count(method, ""))
				for name, value := range map[string][]byte{
					"enableConfigDiscovery":              request.Params.EnableConfigDiscovery,
					"enableFileHooks":                    request.Params.EnableFileHooks,
					"enableOnDemandInstructionDiscovery": request.Params.EnableOnDemandInstructionDiscovery,
					"enableSessionStore":                 request.Params.EnableSessionStore,
					"enableHostGitOperations":            request.Params.EnableHostGitOperations,
				} {
					if disabled {
						require.Equal(t, "false", string(value), "%s must be explicitly false, not omitted/null", name)
					} else {
						require.Empty(t, value, "%s ordinary runtime default must remain omitted", name)
					}
				}
				for name, value := range map[string][]byte{
					"skipCustomInstructions": request.Params.SkipCustomInstructions,
					"skipEmbeddingRetrieval": request.Params.SkipEmbeddingRetrieval,
					"memory":                 request.Params.Memory,
				} {
					if disabled {
						expected := "true"
						if name == "memory" {
							expected = `{"enabled":false}`
						}
						require.JSONEq(t, expected, string(value), name)
					} else {
						require.Empty(t, value, "%s ordinary runtime default must remain omitted", name)
					}
				}
				if disabled {
					select {
					case update := <-captured:
						require.Equal(t, "session.options.update", update.Method)
						require.Equal(t, "true", string(update.Params.SkipCustomInstructions))
					case <-ctx.Done():
						t.Fatal("SDK did not emit the required custom-instruction option update")
					}
					require.Equal(t, 1, server.count("session.options.update", ""))
				} else {
					require.Zero(t, server.count("session.options.update", ""))
				}
			})
		}
	}
}

func (c *runnerTextTransport) ForceStop() { c.interrupted = true }

func TestRunnerTextRuntimeAdmission(t *testing.T) {
	for _, control := range []string{"valid", "nil", "shared", "command-mocks", "ambient-discovery", "no-diagnostics", "unmanaged", "no-force-stop", "startup-error"} {
		t.Run(control, func(t *testing.T) {
			transport := &runnerTextTransport{}
			constructionErr := errors.New("injected CLI path resolution failure")
			engine := &CopilotEngine{
				ownsClient: true, disableCommandMocks: true, disableAmbientDiscovery: true,
				client: manageStartup(transport), diagnostics: &diagnosticState{},
			}
			switch control {
			case "nil":
				engine = nil
			case "shared":
				engine.ownsClient = false
			case "command-mocks":
				engine.disableCommandMocks = false
			case "ambient-discovery":
				engine.disableAmbientDiscovery = false
			case "no-diagnostics":
				engine.diagnostics = nil
			case "unmanaged":
				engine.client = transport
			case "no-force-stop":
				engine.client = manageStartup(&stubClient{})
			case "startup-error":
				engine.client = manageStartup(&startupErrorClient{err: constructionErr})
			}
			err := engine.ValidateRunnerTextRuntime()
			if control == "valid" {
				require.NoError(t, err)
				require.True(t, engine.ForceStopRunnerText())
				require.True(t, transport.interrupted)
			} else {
				require.Error(t, err)
				if control == "startup-error" {
					require.ErrorIs(t, err, constructionErr)
				}
				require.False(t, transport.interrupted, "admission must not start or stop rejected transports")
			}
		})
	}
}
