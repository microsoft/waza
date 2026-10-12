package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
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

func TestScenarioScaffoldConfiguredTaskDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("files:\n  taskGlob: cases/*.task.yaml\n  taskFileSuffix: .task.yaml\n"), 0o600))
	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"new", "eval", "inventory", "--scenario"})
	require.NoError(t, root.Execute())
	evalPath := filepath.Join("evals", "inventory", "eval.yaml")
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	tasks, err := spec.ResolveTestFiles(filepath.Dir(evalPath))
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join("evals", "inventory", "cases", "workflow.task.yaml")}, tasks)
	require.NoDirExists(t, filepath.Join("evals", "inventory", "tasks"))
}

func TestScenarioScaffoldRejectsUnmatchedTaskGlobBeforeWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("files:\n  taskGlob: cases/task-*.yaml\n"), 0o600))
	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"new", "eval", "inventory", "--scenario"})
	require.ErrorContains(t, root.Execute(), "does not match generated task")
	require.NoDirExists(t, "evals")
}

func TestScenarioScaffoldPreflightsEveryOutput(t *testing.T) {
	for _, existing := range []string{"eval.yaml", "tasks/workflow.yaml", "fixtures/inventory.txt"} {
		t.Run(existing, func(t *testing.T) {
			t.Chdir(t.TempDir())
			path := filepath.Join("evals", "inventory", existing)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			original := []byte("keep original bytes\n")
			require.NoError(t, os.WriteFile(path, original, 0o600))
			root := newRootCommand()
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"new", "eval", "inventory", "--scenario"})
			require.ErrorContains(t, root.Execute(), "refusing to overwrite")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, original, after)
			for _, output := range []string{"eval.yaml", "tasks/workflow.yaml", "fixtures/inventory.txt", "fixtures/.gitkeep"} {
				if output != existing {
					require.NoFileExists(t, filepath.Join("evals", "inventory", output))
				}
			}
		})
	}
}

func TestScenarioScaffoldRejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join("evals", "inventory", "eval.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	target := filepath.Join(dir, "unrelated.yaml")
	if err := os.Symlink(target, path); err != nil {
		if scenarioSymlinkPermissionUnavailable(runtime.GOOS, err) {
			t.Skipf("symlink privileges unavailable: %v", err)
		}
		require.NoError(t, err)
	}
	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"new", "eval", "inventory", "--scenario"})
	require.ErrorContains(t, root.Execute(), "refusing to overwrite")
	link, err := os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, target, link)
	require.NoFileExists(t, target)
	require.NoDirExists(t, filepath.Join("evals", "inventory", "fixtures"))
	require.NoDirExists(t, filepath.Join("evals", "inventory", "tasks"))
}

func scenarioSymlinkPermissionUnavailable(goos string, err error) bool {
	var linkErr *os.LinkError
	return goos == "windows" && errors.As(err, &linkErr) && linkErr.Op == "symlink" &&
		(errors.Is(linkErr.Err, fs.ErrPermission) || errors.Is(linkErr.Err, syscall.Errno(1314)))
}

func TestScenarioSymlinkPermissionUnavailable(t *testing.T) {
	permission := &os.LinkError{Op: "symlink", Err: fs.ErrPermission}
	for _, tt := range []struct {
		name, goos string
		err        error
		want       bool
	}{
		{"windows-permission", "windows", permission, true},
		{"windows-privilege", "windows", &os.LinkError{Op: "symlink", Err: syscall.Errno(1314)}, true},
		{"windows-wrapped", "windows", fmt.Errorf("creating link: %w", permission), true},
		{"windows-other-operation", "windows", &os.LinkError{Op: "link", Err: fs.ErrPermission}, false},
		{"windows-existing-entry", "windows", &os.LinkError{Op: "symlink", Err: fs.ErrExist}, false},
		{"windows-missing-path", "windows", &os.LinkError{Op: "symlink", Err: fs.ErrNotExist}, false},
		{"windows-unexpected-error", "windows", &os.LinkError{Op: "symlink", Err: errors.New("unexpected failure")}, false},
		{"windows-untyped-permission", "windows", fs.ErrPermission, false},
		{"windows-nil", "windows", nil, false},
		{"non-windows-permission", "darwin", permission, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, scenarioSymlinkPermissionUnavailable(tt.goos, tt.err))
		})
	}
}

func TestScenarioExclusiveWriteAfterPreflight(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "eval.yaml")
			_, err := os.Lstat(path)
			require.ErrorIs(t, err, fs.ErrNotExist)
			target := filepath.Join(dir, "unrelated.yaml")
			original := []byte("keep competing file bytes\n")
			if symlink {
				if err := os.Symlink(target, path); err != nil {
					if scenarioSymlinkPermissionUnavailable(runtime.GOOS, err) {
						t.Skipf("symlink privileges unavailable: %v", err)
					}
					require.NoError(t, err)
				}
			} else {
				require.NoError(t, os.WriteFile(path, original, 0o600))
			}
			require.ErrorContains(t, writeScenarioFile(path, "new scaffold contents"), "refusing to overwrite")
			if symlink {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				require.Equal(t, target, link)
				require.NoFileExists(t, target)
			} else {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, original, after)
			}
		})
	}
}

func TestScenarioExclusiveWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, writeScenarioFile(path, "scaffold bytes\n"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "scaffold bytes\n", string(data))
	missingParent := filepath.Join(dir, "missing", "eval.yaml")
	err = writeScenarioFile(missingParent, "scaffold bytes\n")
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, "creating")
	require.NoFileExists(t, missingParent)
}
