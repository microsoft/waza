package execution

import (
	"testing"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCommandMockSessionReusedWhenLaterRequestOmitsMocks(t *testing.T) {
	engine := &CopilotEngine{commandMockSessions: make(map[string]*commandmock.Session)}
	workspace := t.TempDir()
	first, err := engine.commandMockSession(workspace, &ExecutionRequest{
		CommandMocks: []models.CommandMockConfig{{
			Name: "az",
			Responses: []models.CommandMockResponse{{
				Args: []string{"account", "show"},
			}},
		}},
		CommandMocksBaseDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, first)
	t.Cleanup(func() {
		_, _ = first.Close()
		_ = commandmock.CloseRuntime()
	})

	second, err := engine.commandMockSession(workspace, &ExecutionRequest{})
	require.NoError(t, err)
	require.Same(t, first, second)
}
