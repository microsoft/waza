package scaffold_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestScenarioFilesConfiguredTaskGlob(t *testing.T) {
	for _, tt := range []struct {
		name, glob, suffix, task string
	}{
		{"default", "tasks/*.yaml", ".yaml", "tasks/workflow.yaml"},
		{"wildcard-default-directory", "t*/*.yaml", ".yaml", "tasks/workflow.yaml"},
		{"wildcard-exact-basename", "*/workflow.task.yaml", ".task.yaml", "tasks/workflow.task.yaml"},
		{"character-class-directory", "[t]asks/*.yaml", ".yaml", "tasks/workflow.yaml"},
		{"normalized-default-directory", "tasks/../tasks/*.yaml", ".yaml", "tasks/workflow.yaml"},
		{"normalized-root-task", "tasks/../*.task.yaml", ".task.yaml", "workflow.task.yaml"},
		{"custom-directory", "cases/*.task.yaml", ".task.yaml", "cases/workflow.task.yaml"},
		{"native-directory-separator", filepath.Join("cases", "*.task.yaml"), ".task.yaml", "cases/workflow.task.yaml"},
		{"nested-directory", "cases/smoke/*.yaml", ".yaml", "cases/smoke/workflow.yaml"},
		{"root-glob", "*.task.yaml", ".task.yaml", "workflow.task.yaml"},
		{"exact-file", "cases/workflow.task.yaml", ".task.yaml", "cases/workflow.task.yaml"},
		{"fixture-yaml-glob", "fixtures/*.yaml", ".yaml", "fixtures/workflow.yaml"},
		{"fixture-task-prefix", "fixtures/w*", ".yaml", "fixtures/workflow.yaml"},
		{"fixture-attribute-glob", "fixtures/*.gitattributes", ".gitattributes", "fixtures/workflow.gitattributes"},
		{"wildcard-attribute-glob", "*/*.gitattributes", ".gitattributes", "tasks/workflow.gitattributes"},
		{"double-star-attribute-glob", "**/*.gitattributes", ".gitattributes", "tasks/workflow.gitattributes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", tt.glob, tt.suffix, "repository")
			require.NoError(t, err)
			require.Contains(t, files, tt.task)
			dir := writeScenarioGlobFiles(t, files)
			evalPath := filepath.Join(dir, "eval.yaml")
			evalErrs, taskErrs, err := validation.ValidateEvalFile(evalPath)
			require.NoError(t, err)
			require.Empty(t, evalErrs)
			require.Empty(t, taskErrs)
			spec, err := models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			tasks, err := spec.ResolveTestFiles(dir)
			require.NoError(t, err)
			require.Equal(t, []string{filepath.Join(dir, filepath.FromSlash(tt.task))}, tasks)
			task, err := models.LoadTestCase(tasks[0])
			require.NoError(t, err)
			require.Equal(t, "inventory-001", task.TestID)
		})
	}
}

func TestScenarioFilesRejectUnusableTaskGlob(t *testing.T) {
	for _, tt := range []struct {
		name, eval, glob, suffix string
	}{
		{"wrong-basename", "eval.yaml", "cases/case-*.yaml", ".yaml"},
		{"wrong-suffix", "eval.yaml", "cases/*.task.yaml", ".yaml"},
		{"wildcard-directory", "eval.yaml", "cases/*/*.yaml", ".yaml"},
		{"wildcard-directory-not-default", "eval.yaml", "c*/*.yaml", ".yaml"},
		{"traversal", "eval.yaml", "../cases/*.yaml", ".yaml"},
		{"absolute", "eval.yaml", "/cases/*.yaml", ".yaml"},
		{"windows-drive", "eval.yaml", "C:/cases/*.yaml", ".yaml"},
		{"invalid-pattern", "eval.yaml", "cases/[.yaml", ".yaml"},
		{"suffix-directory", "eval.yaml", "cases/*.yaml", "/nested.yaml"},
		{"eval-collision", "workflow.yaml", "*.yaml", ".yaml"},
		{"eval-selected-as-task", "eval.yaml", "*.yaml", ".yaml"},
		{"normalized-eval-selected-as-task", "eval.yaml", "tasks/../*.yaml", ".yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles("inventory", "model", tt.eval, tt.glob, tt.suffix, "repository")
			require.Error(t, err)
			require.Nil(t, files)
		})
	}
}

func TestScenarioFilesRejectSelectedNonTaskArtifacts(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		t.Run(template, func(t *testing.T) {
			for _, tc := range []struct {
				name, glob, artifact string
			}{
				{"all-fixtures", "fixtures/*", "fixtures/.gitkeep"},
				{"fixture-extension", "fixtures/*.*", "fixtures/.gitkeep"},
				{"normalized-fixtures", "tasks/../fixtures/*", "fixtures/.gitkeep"},
				{"inventory-only", "fixtures/[wi]*", "fixtures/inventory.txt"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", tc.glob, ".yaml", template)
					if tc.name == "inventory-only" && template != "repository" {
						require.NoError(t, err)
						dir := writeScenarioGlobFiles(t, files)
						evalErrors, taskErrors, err := validation.ValidateEvalFile(filepath.Join(dir, "eval.yaml"))
						require.NoError(t, err)
						require.Empty(t, evalErrors)
						require.Empty(t, taskErrors)
						spec, err := models.LoadEvalSpec(filepath.Join(dir, "eval.yaml"))
						require.NoError(t, err)
						tasks, err := spec.ResolveTestFiles(dir)
						require.NoError(t, err)
						require.Equal(t, []string{filepath.Join(dir, "fixtures", "workflow.yaml")}, tasks)
						_, err = models.LoadTestCase(tasks[0])
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "selects generated non-task file")
						require.ErrorContains(t, err, strconv.Quote(tc.artifact))
						require.ErrorContains(t, err, "narrow the glob")
						require.Nil(t, files)
					}
				})
			}
		})
	}
}

func writeScenarioGlobFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relative, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(contents), 0o600))
	}
	return dir
}
