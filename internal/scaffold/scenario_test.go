package scaffold_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestScenarioTemplatesOutcomes(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		t.Run(template, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", template)
			require.NoError(t, err)
			require.Empty(t, validation.ValidateEvalBytes([]byte(files["eval.yaml"])))
			require.Empty(t, validation.ValidateTaskBytes([]byte(files["tasks/workflow.yaml"])))
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(files["eval.yaml"]), 0o600))
			spec, err := models.LoadEvalSpec(path)
			require.NoError(t, err)
			report := "Inventory total: 3"
			if template == "cli" {
				report = "git version 2.50.0"
			}
			for _, tt := range []struct {
				name, report string
				private      bool
				want         []bool
			}{
				{"good", report, false, []bool{true, true}},
				{"bad", "Inventory total: 4", false, []bool{false, true}},
				{"boundary", report, true, []bool{true, false}},
				{"harness-only", "", false, []bool{false, true}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					workspace := t.TempDir()
					if tt.report != "" {
						require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.txt"), []byte(tt.report), 0o600))
					}
					if tt.private {
						require.NoError(t, os.WriteFile(filepath.Join(workspace, "private.txt"), []byte("sanitized"), 0o600))
					}
					for i, cfg := range spec.Graders {
						grader, err := graders.Create(cfg.Identifier, cfg.Parameters)
						require.NoError(t, err)
						result, err := grader.Grade(t.Context(), &graders.Context{WorkspaceDir: workspace})
						require.NoError(t, err)
						require.Equal(t, tt.want[i], result.Passed)
					}
				})
			}
		})
	}
}

func TestScenarioCheckedInExamples(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		path := filepath.Join("..", "..", "examples", "scenarios", template, "eval.yaml")
		evalErrs, taskErrs, err := validation.ValidateEvalFile(path)
		require.NoError(t, err)
		require.Empty(t, evalErrs)
		require.Empty(t, taskErrs)
		spec, err := models.LoadEvalSpec(path)
		require.NoError(t, err)
		require.Empty(t, spec.SkillName)
		tasks, err := spec.ResolveTestFiles(filepath.Dir(path))
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		for _, taskPath := range tasks {
			_, err := models.LoadTestCase(taskPath)
			require.NoError(t, err)
		}
		generated, err := scaffold.ScenarioFiles(spec.Scenario, spec.Config.ModelID, "eval.yaml", "tasks/*.yaml", ".yaml", template)
		require.NoError(t, err)
		for relative, expected := range generated {
			actual, err := os.ReadFile(filepath.Join(filepath.Dir(path), relative))
			require.NoError(t, err)
			require.Equal(t, expected, strings.ReplaceAll(string(actual), "\r\n", "\n"), "checked-in %s must match the callable scaffold apart from checkout line endings", relative)
		}
	}
}

func TestScenarioFilesRejectInvalidNamesAndTemplates(t *testing.T) {
	for _, name := range []string{"", " \t\n ", ".", "..", "../escape", `..\escape`} {
		t.Run(name, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles(name, "model", "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
			require.Error(t, err)
			require.Nil(t, files)
		})
	}
	files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", "unknown")
	require.ErrorContains(t, err, "unknown scenario template")
	require.Nil(t, files)
}

func TestScenarioFilesQuoteYAMLValues(t *testing.T) {
	name := `inventory: "sanitized"`
	model := `model: "custom"`
	files, err := scaffold.ScenarioFiles(name, model, "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
	require.NoError(t, err)
	require.Empty(t, validation.ValidateEvalBytes([]byte(files["eval.yaml"])))
	require.Empty(t, validation.ValidateTaskBytes([]byte(files["tasks/workflow.yaml"])))
	path := filepath.Join(t.TempDir(), "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(files["eval.yaml"]), 0o600))
	spec, err := models.LoadEvalSpec(path)
	require.NoError(t, err)
	require.Equal(t, name, spec.Scenario)
	require.Equal(t, model, spec.Config.ModelID)
}
