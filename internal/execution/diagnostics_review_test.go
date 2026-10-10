package execution

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDiagnosticSkillHelpersNeverLogPrivateData(t *testing.T) {
	for _, kind := range []string{"skill", "agent", "malformed agent"} {
		t.Run(kind, func(t *testing.T) {
			var log bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(old) })
			source := filepath.Join(t.TempDir(), "credential-secret directory", "private", "provider", "path")
			require.NoError(t, os.MkdirAll(source, 0700))
			content := "---\nname: private-name\ndescription: private-description\n---\nprivate-body\n"
			file := "SKILL.md"
			if kind != "skill" {
				file = "private.agent.md"
			}
			if kind == "malformed agent" {
				content = "---\nname: [credential-secret]\n---\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(source, file), []byte(content), 0600))
			var seen []ExecutionDiagnostic
			e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error { seen = append(seen, d); return nil })
			req := diagnosticRequest(t)
			req.NoSkills, req.SourceDir, req.SkillName = false, source, "private-name"
			if kind != "malformed agent" {
				session := NewMockCopilotSession(gomock.NewController(t))
				client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
					require.NotNil(t, config.SystemMessage)
					require.Contains(t, config.SystemMessage.Content, content)
					return session, nil
				})
				session.EXPECT().SessionID().Return("skill-id")
				session.EXPECT().On(gomock.Any()).Return(func() {}).Times(2)
				session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, nil)
				session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
				session.EXPECT().Disconnect().Return(nil)
				client.EXPECT().DeleteSession(gomock.Any(), "skill-id").Return(nil)
			}
			_, err := e.Execute(t.Context(), req)
			if kind == "malformed agent" {
				require.ErrorContains(t, err, "execute/execute_failed")
				require.NotContains(t, err.Error(), privateDiagnosticSentinel)
				require.NotContains(t, err.Error(), source)
				require.Len(t, seen, 1)
			} else {
				require.NoError(t, err)
			}
			require.NotContains(t, log.String(), source)
			require.NotContains(t, log.String(), "private-name")
			require.NotContains(t, log.String(), "private-body")
			require.NotContains(t, log.String(), privateDiagnosticSentinel)
			client.EXPECT().Stop().Return(nil)
			require.NoError(t, e.Shutdown(t.Context()))
		})
	}
}

func TestDiagnosticFinalizeCommandMocksReportsFailureAndObserverFailure(t *testing.T) {
	for _, observerFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "observer failure"}[observerFails], func(t *testing.T) {
			var seen []ExecutionDiagnostic
			e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
				seen = append(seen, d)
				if observerFails {
					return errors.New(privateDiagnosticSentinel)
				}
				return nil
			})
			workspace := t.TempDir()
			session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
				Name: "private-command", ExpectCalls: utils.Ptr(1),
				Responses: []models.CommandMockResponse{{Args: []string{"test"}, Stdout: "not called"}},
			}}, ".")
			require.NoError(t, err)
			e.commandMockSessions[workspace] = session
			_, err = e.FinalizeCommandMocks(workspace)
			require.ErrorContains(t, err, "commandmock_cleanup/cleanup_failed")
			require.NotContains(t, err.Error(), workspace)
			require.NotContains(t, err.Error(), "private-command")
			require.NotContains(t, err.Error(), privateDiagnosticSentinel)
			if observerFails {
				require.ErrorContains(t, err, "commandmock_cleanup/observer_failed")
			}
			require.Equal(t, []ExecutionDiagnostic{{Stage: StageCommandMockCleanup, Code: CodeCleanup}}, seen)
			require.Empty(t, e.commandMockSessions)
			client.EXPECT().Stop().Return(nil)
			require.NoError(t, e.Shutdown(t.Context()))
			_, err = e.FinalizeCommandMocks(workspace)
			require.ErrorContains(t, err, "commandmock_cleanup/engine_closed")
		})
	}
}

func TestDiagnosticFinalizeCommandMocksDrainsBeforeShutdown(t *testing.T) {
	entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
		require.Equal(t, StageCommandMockCleanup, d.Stage)
		close(entered)
		<-release
		return errors.New(privateDiagnosticSentinel)
	})
	workspace := t.TempDir()
	session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
		Name: "private-command", ExpectCalls: utils.Ptr(1),
		Responses: []models.CommandMockResponse{{Args: []string{"test"}, Stdout: "not called"}},
	}}, ".")
	require.NoError(t, err)
	e.commandMockSessions[workspace] = session
	client.EXPECT().Stop().DoAndReturn(func() error { close(stopped); return nil })
	finalized := make(chan error, 1)
	go func() { _, err := e.FinalizeCommandMocks(workspace); finalized <- err }()
	<-entered
	e.commandMocksMu.Lock()
	require.Empty(t, e.commandMockSessions)
	e.commandMocksMu.Unlock()
	shutdown := make(chan error, 1)
	go func() { shutdown <- e.Shutdown(context.Background()) }()
	select {
	case <-stopped:
		t.Fatal("shutdown missed active finalization after map deletion")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	require.ErrorContains(t, <-finalized, "commandmock_cleanup/observer_failed")
	require.NoError(t, <-shutdown)
}
