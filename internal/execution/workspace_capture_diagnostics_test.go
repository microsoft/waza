package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCaptureWorkspaceDiagnosticsDoNotExposePrivatePaths(t *testing.T) {
	privateHome := filepath.Join(t.TempDir(), "private-home-sentinel")
	require.NoError(t, os.Mkdir(privateHome, 0o700))
	workspace := filepath.Join(privateHome, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0o700))
	for _, test := range []struct {
		name  string
		root  string
		paths []string
	}{
		{"missing root", filepath.Join(privateHome, "missing-workspace"), nil},
		{"absolute target", workspace, []string{filepath.Join(privateHome, "private.txt")}},
		{"escaping target", workspace, []string{"../private-home-sentinel/private.txt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files, err := captureWorkspaceFiles(test.root, test.paths)
			require.Error(t, err)
			require.Nil(t, files)
			require.NotContains(t, err.Error(), privateHome)
			require.NotContains(t, err.Error(), "private-home-sentinel")
			require.Contains(t, err.Error(), "workspace capture incomplete")
			raw, marshalErr := json.Marshal(&ExecutionResponse{
				ErrorMsg: err.Error(), WorkspaceCaptureError: err.Error(),
			})
			require.NoError(t, marshalErr)
			require.NotContains(t, string(raw), "private-home-sentinel")
			if test.name == "missing root" {
				require.ErrorIs(t, err, os.ErrNotExist)
				var pathErr *os.PathError
				require.True(t, errors.As(err, &pathErr))
				require.Contains(t, pathErr.Path, "private-home-sentinel")
			}
		})
	}
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(filepath.Join(privateHome, "outside"), filepath.Join(workspace, "escape")))
		_, err := captureWorkspaceFiles(workspace, []string{"escape/secret"})
		require.Error(t, err)
		require.NotContains(t, err.Error(), privateHome)
		require.NotContains(t, err.Error(), "private-home-sentinel")
	}
}

func TestCaptureWorkspaceDiagnosticNamesAreBounded(t *testing.T) {
	workspace := t.TempDir()
	name := strings.Repeat("a", 200) + ".txt"
	require.NoError(t, os.Mkdir(filepath.Join(workspace, name), 0o700))
	_, err := captureWorkspaceFiles(workspace, []string{name})
	require.Error(t, err)
	require.Less(t, len(err.Error()), 400)
	require.NotContains(t, err.Error(), "\n")
	require.Contains(t, err.Error(), "not a regular file")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "valid.txt"), []byte("valid"), 0o600))
	files, err := captureWorkspaceFiles(workspace, []string{"valid.txt", "missing.txt"})
	require.NoError(t, err)
	require.Equal(t, []byte("valid"), files["valid.txt"])
}

func TestCaptureIODiagnosticPreservesCauseWithoutPrintingIt(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		code  string
	}{
		{"missing", os.ErrNotExist, "not_found"},
		{"permission", os.ErrPermission, "permission"},
		{"closed", os.ErrClosed, "closed"},
		{"other", errors.New("private-home-sentinel provider error"), "io"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := &os.PathError{Op: "private operation", Path: "/private-home-sentinel/file", Err: test.cause}
			for _, operation := range []string{"open workspace root", "inspect", "open", "read", "close", "open directory", "inspect directory", "read directory", "close directory"} {
				err := captureIOError(operation, "logical\nfile.txt", cause)
				require.ErrorIs(t, err, test.cause)
				var original *os.PathError
				require.ErrorAs(t, err, &original)
				require.Same(t, cause, original)
				require.Contains(t, err.Error(), "["+test.code+"]")
				require.Contains(t, err.Error(), operation)
				require.Contains(t, err.Error(), `"logical\nfile.txt"`)
				require.NotContains(t, err.Error(), "\n")
				require.NotContains(t, err.Error(), "private-home-sentinel")
			}
		})
	}
	require.NotContains(t, captureIOError("open", "/private-home-sentinel/file", os.ErrPermission).Error(), "private-home-sentinel")
	require.NotContains(t, captureIOError("open", "../private-home-sentinel/file", os.ErrPermission).Error(), "private-home-sentinel")
	require.Less(t, len(captureIOError("open", strings.Repeat("\x00", 1000), os.ErrPermission).Error()), 900)
}

func TestCaptureFailureRuntimeCauseDoesNotChangeResponseJSON(t *testing.T) {
	cause := captureIOError("open workspace root", "", &os.PathError{
		Op: "open", Path: "/private-home-sentinel", Err: os.ErrNotExist,
	})
	resp := &ExecutionResponse{WorkspaceCaptureError: cause.Error()}
	withoutCause, err := json.Marshal(resp)
	require.NoError(t, err)
	require.EqualError(t, resp.CaptureFailure(), cause.Error())
	resp.WorkspaceCaptureCause = cause
	require.ErrorIs(t, resp.CaptureFailure(), os.ErrNotExist)
	withCause, err := json.Marshal(resp)
	require.NoError(t, err)
	require.Equal(t, withoutCause, withCause)
	require.NotContains(t, string(withCause), "private-home-sentinel")
	resp.WorkspaceCaptureError = ""
	require.NoError(t, resp.CaptureFailure())
}

func TestExecuteRecordedCaptureIODiagnosticsExcludePrivateRoot(t *testing.T) {
	for _, sendFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "execution error"}[sendFails], func(t *testing.T) {
			engine, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return nil })
			session := NewMockCopilotSession(gomock.NewController(t))
			client.EXPECT().Start(gomock.Any()).Return(nil)
			client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil)
			client.EXPECT().Stop().Return(nil)
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
			client.EXPECT().DeleteSession(gomock.Any(), "private-capture").Return(nil)
			session.EXPECT().SessionID().Return("private-capture")
			session.EXPECT().Disconnect().Return(nil)
			session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
			var handlers []copilot.SessionEventHandler
			session.EXPECT().On(gomock.Any()).DoAndReturn(func(handler copilot.SessionEventHandler) func() {
				handlers = append(handlers, handler)
				return func() {}
			}).AnyTimes()
			req := diagnosticRequest(t)
			req.WorkspaceDir = filepath.Join(t.TempDir(), "private-home-sentinel")
			require.NoError(t, os.Mkdir(req.WorkspaceDir, 0o700))
			req.SkipWorkspaceCapture = false
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
				require.NoError(t, os.Rename(req.WorkspaceDir, req.WorkspaceDir+".moved"))
				event := &copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "authored private-home-sentinel output"}}
				for _, handler := range handlers {
					handler(*event)
				}
				if sendFails {
					return nil, context.DeadlineExceeded
				}
				return event, nil
			})
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			ctx, scope := NewUsageScope(t.Context())
			resp, err := ExecuteRecorded(ctx, engine, req)
			if sendFails {
				var diagnostic *DiagnosticError
				require.ErrorAs(t, err, &diagnostic)
				require.NotContains(t, err.Error(), "private-home-sentinel")
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, resp)
			require.False(t, resp.Success)
			require.Equal(t, !sendFails, resp.ExecutionSucceeded)
			require.NotContains(t, resp.ErrorMsg, "private-home-sentinel")
			require.NotContains(t, resp.WorkspaceCaptureError, "private-home-sentinel")
			require.Contains(t, resp.WorkspaceCaptureError, "[not_found]")
			require.ErrorIs(t, resp.CaptureFailure(), os.ErrNotExist)
			var pathErr *os.PathError
			require.ErrorAs(t, resp.CaptureFailure(), &pathErr)
			require.Equal(t, req.WorkspaceDir, pathErr.Path)
			require.Nil(t, resp.WorkspaceFiles)
			require.Contains(t, resp.FinalOutput, "authored private-home-sentinel output")
			require.NotEmpty(t, resp.Events)
			require.NotNil(t, resp.Usage)
			require.Len(t, scope.Snapshot().Sessions, 1)
			raw, marshalErr := json.Marshal(resp)
			require.NoError(t, marshalErr)
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &public))
			require.NotContains(t, string(public["ErrorMsg"]), "private-home-sentinel")
			require.NotContains(t, string(public["WorkspaceCaptureError"]), "private-home-sentinel")
			require.NotContains(t, public, "WorkspaceCaptureCause")
		})
	}
}
