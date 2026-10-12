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
				{"trailing-lf", report + "\n", false, []bool{true, true}},
				{"trailing-crlf", report + "\r\n", false, []bool{true, true}},
				{"bad", "Inventory total: 4", false, []bool{false, true}},
				{"near-miss-thirty", "Inventory total: 30", false, []bool{false, true}},
				{"extra-prefix", "Incorrect: Inventory total: 3", false, []bool{false, true}},
				{"extra-suffix", "Inventory total: 3 incorrect", false, []bool{false, true}},
				{"extra-newline", "Inventory total: 3\n\n", false, []bool{false, true}},
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", tt.glob, tt.suffix, "repository")
			require.NoError(t, err)
			require.Contains(t, files, tt.task)
			dir := t.TempDir()
			for path, contents := range files {
				full := filepath.Join(dir, filepath.FromSlash(path))
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
				require.NoError(t, os.WriteFile(full, []byte(contents), 0o600))
			}
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
