package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityMockRequestOverrides(t *testing.T) {
	root := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1")
	spec, err := models.LoadEvalSpec(filepath.Join(root, "eval-1.4.yaml"))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "task.yaml"))
	require.NoError(t, err)
	for _, c := range []struct {
		name, suffix, command string
		count                 int
	}{
		{"inherit", "", "compat-cli", 1},
		{"empty replaces", "\ncommand_mocks: []\n", "", 0},
		{"populated replaces", "\ncommand_mocks:\n  - name: task-cli\n    responses:\n      - args: [inspect]\n        stdout: ready\n", "task-cli", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "task.yaml")
			require.NoError(t, os.WriteFile(path, append(data, []byte(c.suffix)...), 0o600))
			tc, err := models.LoadTestCase(path)
			require.NoError(t, err)
			require.NoError(t, tc.ValidateForExecutor("copilot-sdk"))
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(root)), nil)
			req, err := runner.buildExecutionRequest(tc)
			require.NoError(t, err)
			require.Len(t, req.CommandMocks, c.count)
			require.Equal(t, root, req.CommandMocksBaseDir)
			require.Equal(t, "Say ready.", req.Message)
			require.False(t, req.SuppressSkillBody)
			if c.count > 0 {
				require.Equal(t, c.command, req.CommandMocks[0].Name)
			} else {
				require.NotNil(t, req.CommandMocks, "explicit empty override must not become inheritance")
			}
			require.Equal(t, "compat-cli", spec.CommandMocks[0].Name, "task override must not mutate eval")
		})
	}
}
