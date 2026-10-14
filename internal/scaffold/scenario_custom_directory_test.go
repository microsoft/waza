package scaffold_test

import (
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestScenarioCustomDirectoryDiscoveryAllTemplates(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		t.Run(template, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "cases/*.task.yaml", ".task.yaml", template)
			require.NoError(t, err)
			dir := writeScenarioFiles(t, files)
			evalPath := filepath.Join(dir, "eval.yaml")
			evalErrors, taskErrors, err := validation.ValidateEvalFile(evalPath)
			require.NoError(t, err)
			require.Empty(t, evalErrors)
			require.Empty(t, taskErrors)
			spec, err := models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			paths, err := spec.ResolveTestFiles(dir)
			require.NoError(t, err)
			require.Equal(t, []string{filepath.Join(dir, "cases", "workflow.task.yaml")}, paths)
			task, err := models.LoadTestCase(paths[0])
			require.NoError(t, err)
			require.Equal(t, "inventory-001", task.TestID)
		})
	}
}
