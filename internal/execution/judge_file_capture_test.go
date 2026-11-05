package execution_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
	"github.com/stretchr/testify/require"
)

func TestJudgeFilesAreCapturedBeforeActorDisconnect(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("secure selected-file capture requires Linux/macOS")
	}
	client := &copilotfake.Client{
		Send: func(config *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
			require.NoError(t, os.WriteFile(filepath.Join(config.WorkingDirectory, "report.txt"), []byte("ACTOR_FINAL_CANARY"), 0600))
			return []copilot.SessionEvent{
				{Data: &copilot.UserMessageData{Content: options.Prompt}},
				{Data: &copilot.AssistantMessageData{Content: "done"}},
			}, nil
		},
		Disconnect: func(config *copilot.SessionConfig) error {
			return os.WriteFile(filepath.Join(config.WorkingDirectory, "report.txt"), []byte("DISCONNECT_RESTORED_CANARY"), 0600)
		},
	}
	engine := client.Engine()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
	response, err := engine.Execute(t.Context(), &execution.ExecutionRequest{
		Message: "Write report", NoSkills: true, SkipWorkspaceCapture: true,
		CaptureJudgeFiles: func(root string) *models.JudgeFileCapture {
			return judgeevidence.CaptureFiles(root, models.PromptEvidenceContext{Files: []string{"report.txt"}})
		},
	})
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Empty(t, response.WorkspaceFiles)
	require.NotNil(t, response.JudgeFiles)
	require.Empty(t, response.JudgeFiles.Diagnostics)
	require.Len(t, response.JudgeFiles.Files, 1)
	require.Equal(t, "ACTOR_FINAL_CANARY", response.JudgeFiles.Files[0].Content)
	restored, err := os.ReadFile(filepath.Join(response.WorkspaceDir, "report.txt"))
	require.NoError(t, err)
	require.Equal(t, "DISCONNECT_RESTORED_CANARY", string(restored))
}
