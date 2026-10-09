package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNoSkillsDoesNotReintroduceSourceDirectory(t *testing.T) {
	for _, sessionID := range []string{"", "resumed"} {
		t.Run("session="+sessionID, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: ambient\ndescription: Ambient\n---\nUnrelated behavior."), 0o600))
			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			stopped := errors.New("stop before any model call")
			if sessionID == "" {
				client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, cfg *copilot.SessionConfig) (CopilotSession, error) {
						require.Empty(t, cfg.SkillDirectories)
						require.NotNil(t, cfg.EnableSkills)
						require.False(t, *cfg.EnableSkills)
						require.Nil(t, cfg.SystemMessage)
						return nil, stopped
					})
			} else {
				client.EXPECT().ResumeSessionWithOptions(gomock.Any(), sessionID, gomock.Any()).DoAndReturn(
					func(_ context.Context, _ string, cfg *copilot.ResumeSessionConfig) (CopilotSession, error) {
						require.Empty(t, cfg.SkillDirectories)
						require.NotNil(t, cfg.EnableSkills)
						require.False(t, *cfg.EnableSkills)
						require.Nil(t, cfg.SystemMessage)
						return nil, stopped
					})
			}
			engine := NewCopilotEngineBuilder("model", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
			}).Build()
			require.NoError(t, engine.Initialize(t.Context()))
			defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
			_, err := engine.Execute(t.Context(), &ExecutionRequest{
				Message: "hello", SessionID: sessionID, SourceDir: dir, SkillPaths: []string{dir},
				SkillName: "ambient", NoSkills: true, WorkspaceDir: t.TempDir(),
			})
			require.ErrorIs(t, err, stopped)
		})
	}
}
