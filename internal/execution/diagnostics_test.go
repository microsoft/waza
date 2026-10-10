package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const privateDiagnosticSentinel = "credential-secret /private/provider/path"

func diagnosticTestEngine(t *testing.T, observer func(ExecutionDiagnostic) error) (*CopilotEngine, *MockCopilotClient) {
	t.Helper()
	client := NewMockCopilotClient(gomock.NewController(t))
	e := NewCopilotEngineBuilder("requested-model", &CopilotEngineBuilderOptions{
		NewCopilotClient:   func(*copilot.ClientOptions) CopilotClient { return client },
		DiagnosticObserver: observer,
	}).Build()
	e.provider = customProviderConfig{}
	return e, client
}

func completeDiagnosticMetrics() *rpc.UsageGetMetricsResult {
	return &rpc.UsageGetMetricsResult{
		TotalNanoAiu: utils.Ptr(0.0),
		ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
			"observed-model": {TotalNanoAiu: utils.Ptr(0.0), Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 7}},
		},
	}
}

func diagnosticRequest(t *testing.T) *ExecutionRequest {
	t.Helper()
	return &ExecutionRequest{WorkspaceDir: t.TempDir(), NoSkills: true, EphemeralSession: true, SkipWorkspaceCapture: true}
}

func TestDiagnosticInitializePersistsAndSanitizes(t *testing.T) {
	for _, failure := range []string{"provider", "start", "auth", "unauthenticated", "nil auth"} {
		for _, observerFails := range []bool{false, true} {
			t.Run(failure+map[bool]string{false: "", true: "/observer"}[observerFails], func(t *testing.T) {
				var seen []ExecutionDiagnostic
				e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
					seen = append(seen, d)
					if observerFails {
						return errors.New(privateDiagnosticSentinel)
					}
					return nil
				})
				raw := errors.New(privateDiagnosticSentinel)
				switch failure {
				case "provider":
					e.provider.err = raw
				case "start":
					client.EXPECT().Start(gomock.Any()).Return(raw)
				default:
					client.EXPECT().Start(gomock.Any()).Return(nil)
					var auth *copilot.GetAuthStatusResponse
					var err error
					if failure == "auth" {
						err = raw
					}
					if failure != "nil auth" {
						auth = &copilot.GetAuthStatusResponse{}
					}
					client.EXPECT().GetAuthStatus(gomock.Any()).Return(auth, err)
					client.EXPECT().Stop().Return(raw)
				}
				err := e.Initialize(t.Context())
				require.Error(t, err)
				require.NotContains(t, err.Error(), privateDiagnosticSentinel)
				require.Equal(t, err, e.Initialize(t.Context()))
				require.NotEmpty(t, seen)
				if observerFails {
					require.Contains(t, err.Error(), "observer_failed")
				}
			})
		}
	}
}

func TestDiagnosticOperationFailuresAreInvocationLocal(t *testing.T) {
	for _, failure := range []string{"create", "resume", "send", "disconnect", "delete", "metrics", "partial", "model", "shutdown usage"} {
		for _, observerFails := range []bool{false, true} {
			t.Run(failure+map[bool]string{false: "", true: "/observer"}[observerFails], func(t *testing.T) {
				var log bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
				t.Cleanup(func() { slog.SetDefault(old) })
				var seen []ExecutionDiagnostic
				e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
					seen = append(seen, d)
					if observerFails {
						return errors.New(privateDiagnosticSentinel)
					}
					return nil
				})
				req := diagnosticRequest(t)
				raw := errors.New(privateDiagnosticSentinel)
				session := NewMockCopilotSession(gomock.NewController(t))
				switch failure {
				case "create":
					client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(nil, raw)
				case "resume":
					req.SessionID = "remote-private-id"
					client.EXPECT().ResumeSessionWithOptions(gomock.Any(), req.SessionID, gomock.Any()).Return(nil, raw)
				default:
					client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
					session.EXPECT().SessionID().Return("private-id")
					session.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
					sendErr, disconnectErr, deleteErr := error(nil), error(nil), error(nil)
					if failure == "send" {
						sendErr = raw
					}
					if failure == "disconnect" {
						disconnectErr = raw
					}
					if failure == "delete" {
						deleteErr = raw
					}
					session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, sendErr)
					session.EXPECT().Disconnect().Return(disconnectErr)
					client.EXPECT().DeleteSession(gomock.Any(), "private-id").Return(deleteErr)
					metrics := completeDiagnosticMetrics()
					if failure == "partial" {
						metrics.TotalNanoAiu = nil
					}
					if failure == "model" {
						metrics.ModelMetrics = nil
					}
					if failure == "metrics" || failure == "shutdown usage" {
						session.EXPECT().UsageMetrics(gomock.Any()).Return(nil, raw)
						if failure == "shutdown usage" {
							session.EXPECT().ShutdownUsage(gomock.Any()).Return(nil, raw)
						} else {
							session.EXPECT().ShutdownUsage(gomock.Any()).Return(&copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(0.0)}, nil)
						}
					} else {
						session.EXPECT().UsageMetrics(gomock.Any()).Return(metrics, nil)
					}
				}
				resp, err := e.Execute(t.Context(), req)
				require.Error(t, err)
				require.NotContains(t, err.Error(), privateDiagnosticSentinel)
				require.NotContains(t, log.String(), privateDiagnosticSentinel)
				if resp != nil {
					require.False(t, resp.Success)
					require.NotContains(t, resp.ErrorMsg, privateDiagnosticSentinel)
				}
				require.NotEmpty(t, seen)
				for _, d := range seen {
					data, err := json.Marshal(d)
					require.NoError(t, err)
					require.NotContains(t, string(data), "private-id")
					require.NotContains(t, string(data), "SessionID")
				}
				if observerFails {
					require.Contains(t, err.Error(), "observer_failed")
				}
				if failure == "delete" {
					client.EXPECT().DeleteSession(gomock.Any(), "private-id").Return(nil)
				}
				client.EXPECT().Stop().Return(nil)
				require.NoError(t, e.Shutdown(t.Context()))
			})
		}
	}
}

func TestDiagnosticShutdownDrainsAndRejectsSameSession(t *testing.T) {
	var mu sync.Mutex
	var seen []ExecutionDiagnostic
	e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, d)
		return nil
	})
	req := diagnosticRequest(t)
	req.SessionID = "resumed-remote"
	session := NewMockCopilotSession(gomock.NewController(t))
	client.EXPECT().ResumeSessionWithOptions(gomock.Any(), req.SessionID, gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return(req.SessionID)
	session.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
	sending, releaseSend, disconnecting, releaseDisconnect, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
		close(sending)
		<-releaseSend
		return nil, nil
	})
	session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
	session.EXPECT().Disconnect().DoAndReturn(func() error { close(disconnecting); <-releaseDisconnect; return nil })
	client.EXPECT().Stop().DoAndReturn(func() error { close(stopped); return nil })
	executed := make(chan error, 1)
	go func() { _, err := e.Execute(context.Background(), req); executed <- err }()
	<-sending
	_, err := e.Execute(t.Context(), req)
	require.ErrorContains(t, err, "session_busy")
	shutdown := make(chan error, 1)
	go func() { shutdown <- e.Shutdown(context.Background()) }()
	deadline := time.After(5 * time.Second)
	for {
		e.diagnostics.mu.Lock()
		closed := e.diagnostics.closed
		e.diagnostics.mu.Unlock()
		if closed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not close admissions")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	_, err = e.Execute(t.Context(), diagnosticRequest(t))
	require.ErrorContains(t, err, "engine_closed")
	close(releaseSend)
	<-disconnecting
	select {
	case <-stopped:
		t.Fatal("client stopped before deferred cleanup")
	default:
	}
	close(releaseDisconnect)
	require.NoError(t, <-executed)
	require.NoError(t, <-shutdown)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 2)
	// No DeleteSession expectation: resumed ephemeral remote data is never owned.
}

func TestDiagnosticShutdownAggregatesAndContinuesCleanup(t *testing.T) {
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return errors.New(privateDiagnosticSentinel) })
	workspace := filepath.Join(t.TempDir(), "owned")
	require.NoError(t, os.Mkdir(workspace, 0700))
	e.workspaces = []string{workspace}
	e.diagnostics.pending["private-id"] = true
	client.EXPECT().DeleteSession(gomock.Any(), "private-id").Return(errors.New(privateDiagnosticSentinel))
	client.EXPECT().Stop().Return(errors.New(privateDiagnosticSentinel))
	err := e.Shutdown(t.Context())
	require.ErrorContains(t, err, "shutdown_delete/cleanup_failed")
	require.ErrorContains(t, err, "client_stop/cleanup_failed")
	require.ErrorContains(t, err, "observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
	require.NoDirExists(t, workspace)
	require.Equal(t, err, e.Shutdown(t.Context()))
}

func TestDiagnosticObserverFailureEveryStage(t *testing.T) {
	for _, stage := range []DiagnosticStage{StageInitialize, StageExecute, StageDisconnect, StageEphemeralDelete, StageUsageMetrics, StageShutdownUsage,
		StageShutdownDelete, StageClientStop, StageCommandMockCleanup, StageGitCleanup, StageWorkspaceCleanup} {
		t.Run(string(stage), func(t *testing.T) {
			op := &diagnosticOperation{state: &diagnosticState{observer: func(d ExecutionDiagnostic) error {
				require.Equal(t, stage, d.Stage)
				require.Equal(t, CodeCleanup, d.Code)
				return errors.New(privateDiagnosticSentinel)
			}}}
			op.report(stage, CodeCleanup, "private-id")
			require.ErrorContains(t, op.err(), string(stage)+"/cleanup_failed")
			require.ErrorContains(t, op.err(), string(stage)+"/observer_failed")
			require.NotContains(t, op.err().Error(), privateDiagnosticSentinel)
		})
	}
	err := &DiagnosticError{Diagnostic: ExecutionDiagnostic{Stage: DiagnosticStage(privateDiagnosticSentinel), Code: DiagnosticCode(privateDiagnosticSentinel)}}
	require.Equal(t, "copilot diagnostic: execute/execute_failed", err.Error())
}

type diagnosticFailingGitResource struct{}

func (diagnosticFailingGitResource) Cleanup(context.Context) error {
	return errors.New(privateDiagnosticSentinel)
}

func TestDiagnosticShutdownNativeCleanupFailures(t *testing.T) {
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return errors.New(privateDiagnosticSentinel) })
	parentFile := filepath.Join(t.TempDir(), "private-file")
	require.NoError(t, os.WriteFile(parentFile, nil, 0600))
	e.workspaces = []string{filepath.Join(parentFile, "impossible-child")}
	e.gitResources = []GitResource{diagnosticFailingGitResource{}}
	session, err := commandmock.NewSession(t.TempDir(), []models.CommandMockConfig{{
		Name: "diagnostic-mock", ExpectCalls: utils.Ptr(1),
		Responses: []models.CommandMockResponse{{Args: []string{"test"}, Stdout: "not called"}},
	}}, ".")
	require.NoError(t, err)
	e.commandMockSessions["workspace"] = session
	client.EXPECT().Stop().Return(nil)
	err = e.Shutdown(t.Context())
	require.ErrorContains(t, err, "commandmock_cleanup/cleanup_failed")
	require.ErrorContains(t, err, "git_cleanup/cleanup_failed")
	require.ErrorContains(t, err, "workspace_cleanup/cleanup_failed")
	require.ErrorContains(t, err, "observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
	require.NotContains(t, err.Error(), parentFile)
}

func TestDiagnosticMissingSessionIDDoesNotCreateLedgerEntry(t *testing.T) {
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return nil })
	session := NewMockCopilotSession(gomock.NewController(t))
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("")
	session.EXPECT().Disconnect().Return(nil)
	_, err := e.Execute(t.Context(), diagnosticRequest(t))
	require.ErrorContains(t, err, "execute_failed")
	require.Empty(t, e.usageCollectors)
	require.Empty(t, e.diagnostics.busy)
	require.Empty(t, e.diagnostics.pending)
	client.EXPECT().Stop().Return(nil)
	require.NoError(t, e.Shutdown(t.Context()))
}

func TestNewOwnedCopilotClientDoesNotUseSharedClient(t *testing.T) {
	a := NewOwnedCopilotClient(&copilot.ClientOptions{})
	b := NewOwnedCopilotClient(&copilot.ClientOptions{})
	require.NotSame(t, a, b)
	wa, ok := a.(*copilotClientWrapper)
	require.True(t, ok)
	require.NotNil(t, wa.inner)
}

func TestDiagnosticInitializeSuccessAndNilObserverCompatibility(t *testing.T) {
	t.Run("enabled success", func(t *testing.T) {
		e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { t.Fatal("unexpected diagnostic"); return nil })
		client.EXPECT().Start(gomock.Any()).Return(nil)
		client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil)
		require.NoError(t, e.Initialize(t.Context()))
		require.NoError(t, e.Initialize(t.Context()))
		client.EXPECT().Stop().Return(nil)
		require.NoError(t, e.Shutdown(t.Context()))
	})
	t.Run("nil observer retains legacy local init error", func(t *testing.T) {
		e, client := diagnosticTestEngine(t, nil)
		require.Nil(t, e.diagnostics)
		client.EXPECT().Start(gomock.Any()).Return(errors.New(privateDiagnosticSentinel))
		require.ErrorContains(t, e.Initialize(t.Context()), privateDiagnosticSentinel)
		require.NoError(t, e.Initialize(t.Context()))
		client.EXPECT().Stop().Return(nil)
		require.NoError(t, e.Shutdown(t.Context()))
	})
}

func TestDiagnosticExplicitDeleteRetainsFailedOwnershipAndSanitizes(t *testing.T) {
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return errors.New(privateDiagnosticSentinel) })
	session := NewMockCopilotSession(gomock.NewController(t))
	e.sessions = map[string]CopilotSession{"owned": session}
	e.usageCollectors = map[string]*SessionUsageCollector{"owned": NewSessionUsageCollector()}
	session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil).Times(2)
	client.EXPECT().DeleteSession(gomock.Any(), "owned").Return(errors.New(privateDiagnosticSentinel))
	err := e.DeleteSession(t.Context(), "owned")
	require.ErrorContains(t, err, "shutdown_delete/cleanup_failed")
	require.ErrorContains(t, err, "observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
	require.Contains(t, e.sessions, "owned")
	client.EXPECT().DeleteSession(gomock.Any(), "owned").Return(nil)
	require.NoError(t, e.DeleteSession(t.Context(), "owned"))
	require.Empty(t, e.sessions)
	require.NoError(t, e.DeleteSession(t.Context(), ""))
	client.EXPECT().Stop().Return(nil)
	require.NoError(t, e.Shutdown(t.Context()))
	require.ErrorContains(t, e.DeleteSession(t.Context(), "owned"), "engine_closed")
}

func TestDiagnosticAnonymousInvocationsAndShutdownKeepSeparateAggregates(t *testing.T) {
	observerCalls := 0
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error {
		observerCalls++
		if observerCalls == 1 {
			return errors.New(privateDiagnosticSentinel)
		}
		return nil
	})
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(func(context.Context, *copilot.SessionConfig) (CopilotSession, error) {
		arrived <- struct{}{}
		<-release
		return nil, errors.New(privateDiagnosticSentinel)
	})
	requests := []*ExecutionRequest{diagnosticRequest(t), diagnosticRequest(t)}
	results := []chan error{make(chan error, 1), make(chan error, 1)}
	for i, req := range requests {
		go func() { _, err := e.Execute(context.Background(), req); results[i] <- err }()
	}
	<-arrived
	<-arrived
	close(release)
	observerFailures := 0
	for _, result := range results {
		err := <-result
		require.ErrorContains(t, err, "execute/create_failed")
		require.NotContains(t, err.Error(), privateDiagnosticSentinel)
		require.NotContains(t, err.Error(), "client_stop")
		if strings.Contains(err.Error(), "observer_failed") {
			observerFailures++
		}
	}
	require.Equal(t, 1, observerFailures)
	require.Empty(t, e.usageCollectors)
	require.Empty(t, e.diagnostics.busy)
	require.Empty(t, e.diagnostics.pending)
	client.EXPECT().Stop().Return(errors.New(privateDiagnosticSentinel))
	err := e.Shutdown(t.Context())
	require.ErrorContains(t, err, "client_stop/cleanup_failed")
	require.NotContains(t, err.Error(), "create_failed")
	require.NotContains(t, err.Error(), "observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
}
