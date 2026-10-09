package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestScenarioScaffold(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		t.Run(template, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			require.NoError(t, os.WriteFile(".waza.yaml", []byte("defaults:\n  engine: mock\nfiles:\n  evalFile: suite.yml\n  taskGlob: tasks/*.task.yaml\n  taskFileSuffix: .task.yaml\n"), 0o600))
			root := newRootCommand()
			root.SetOut(&bytes.Buffer{})
			root.SetArgs([]string{"new", "eval", "inventory", "--scenario", "--template", template})
			require.NoError(t, root.Execute())
			path := filepath.Join(dir, "evals/inventory/suite.yml")
			spec, err := models.LoadEvalSpec(path)
			require.NoError(t, err)
			require.Equal(t, "inventory", spec.Scenario)
			require.Empty(t, spec.SkillName)
			require.Equal(t, "copilot-sdk", spec.Config.EngineType)
			evalErrs, taskErrs, err := validation.ValidateEvalFile(path)
			require.NoError(t, err)
			require.Empty(t, evalErrs)
			require.Empty(t, taskErrs)
			tasks, err := spec.ResolveTestFiles(filepath.Dir(path))
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			_, err = models.LoadTestCase(tasks[0])
			require.NoError(t, err)
			require.NoFileExists(t, filepath.Join(dir, "SKILL.md"))
			root = newRootCommand()
			root.SetArgs([]string{"new", "eval", "inventory", "--scenario", "--template", template})
			require.ErrorContains(t, root.Execute(), "refusing to overwrite")
		})
	}
}

func TestScenarioScaffoldInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"new", "eval", "inventory", "--template", "cli"},
		{"new", "eval", "inventory", "--scenario", "--template", "unknown"},
		{"new", "eval", "../escape", "--scenario"},
	} {
		t.Run(args[2]+args[len(args)-1], func(t *testing.T) {
			t.Chdir(t.TempDir())
			root := newRootCommand()
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(args)
			require.Error(t, root.Execute())
			require.NoDirExists(t, "evals")
		})
	}

}

func TestScenarioDoesNotClaimSkillCoverage(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, filepath.Join("skills", "inventory"), "inventory")
	writeEval(t, dir, filepath.Join("evals", "inventory", "eval.yaml"), "scenario: inventory\nschemaVersion: \"2.0\"\ntasks: [tasks/*.yaml]\n")
	report, err := buildCoverageReport(dir, nil)
	require.NoError(t, err)
	require.Equal(t, 1, report.Uncovered)
	require.Equal(t, 0, report.Covered)
}

func TestScenarioMigrateNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval.yaml")
	data := []byte("name: inventory\nscenario: inventory\nschemaVersion: \"2.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	var out bytes.Buffer
	require.NoError(t, runMigrate(&out, path))
	require.Contains(t, out.String(), "supported scenario eval")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, after)
}
