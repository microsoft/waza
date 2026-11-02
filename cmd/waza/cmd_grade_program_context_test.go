package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

const (
	programContextCopyEnv = "WAZA_TEST_PROGRAM_CONTEXT_COPY"
	programContextLockEnv = "WAZA_TEST_PROGRAM_CONTEXT_LOCK"
)

// errDeletionNotDenied reports that the current account can delete an entry
// the fixture tried to lock (for example, an elevated Windows runner).
var errDeletionNotDenied = errors.New("the current account can delete the locked entry")

// requireUndeletableEntries skips the test when this environment cannot make
// deletion fail (root, or an elevated Windows account that bypasses the deny
// ACEs), so the cleanup-failure path cannot be exercised end to end here.
// Removal failures stay covered on every platform by the hook-injected tests
// in internal/graders.
func requireUndeletableEntries(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	err := lockUndeletableEntry(probe)
	if errors.Is(err, errDeletionNotDenied) {
		t.Skipf("cannot make deletion fail in this environment: %v", err)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unlockUndeletableEntry(probe)) })
	if os.Remove(filepath.Join(probe, "f")) == nil {
		t.Skipf("cannot make deletion fail in this environment: %v", errDeletionNotDenied)
	}
}

// TestProgramGradingContextCopyHelper runs as a program grader and copies the
// grading context file so the parent test can inspect it.
func TestProgramGradingContextCopyHelper(t *testing.T) {
	dest := os.Getenv(programContextCopyEnv)
	if dest == "" {
		return
	}
	err := copyProgramGradingContext(dest)
	status := "ready"
	if err != nil {
		status = err.Error()
	}
	// Program exit failures are ordinary failed grades, not CLI errors. Report
	// fixture setup independently so they cannot impersonate cleanup failures.
	if statusErr := os.WriteFile(dest+".status", []byte(status), 0o600); statusErr != nil {
		err = errors.Join(err, fmt.Errorf("reporting fixture setup: %w", statusErr))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Print("context copied")
	os.Exit(0)
}

func copyProgramGradingContext(dest string) error {
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return fmt.Errorf("reading grader input: %w", err)
	}
	contextPath := os.Getenv(graders.ProgramGradingContextEnv)
	data, err := os.ReadFile(contextPath)
	if err != nil {
		return fmt.Errorf("reading grading context: %w", err)
	}
	if err := errors.Join(
		os.WriteFile(dest, data, 0o600),
		os.WriteFile(dest+".path", []byte(contextPath), 0o600),
	); err != nil {
		return fmt.Errorf("copying grading context: %w", err)
	}
	if os.Getenv(programContextLockEnv) != "" {
		// Leave an entry in the context directory that the grader's own user
		// cannot delete, so Waza's cleanup fails.
		if err := lockUndeletableEntry(filepath.Join(filepath.Dir(contextPath), "locked")); err != nil {
			return fmt.Errorf("setting up cleanup failure: %w", err)
		}
	}
	return nil
}

func writeProgramContextTask(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", fmt.Sprintf(`id: task-program
name: Program context
inputs:
  prompt: "Check account"
graders:
  - name: context_check
    type: program
    config:
      command: '%s'
      args: ["-test.run=^TestProgramGradingContextCopyHelper$"]
      timeout: 60
`, strings.ReplaceAll(exe, "'", "''")))
	return specPath
}

func TestGradeCommand_ProgramGraderContextCleanupFailureFailsGrade(t *testing.T) {
	requireUndeletableEntries(t)
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "context-copy.json")
	t.Setenv(programContextCopyEnv, copyPath)
	t.Setenv(programContextLockEnv, "1")
	specPath := writeProgramContextTask(t, dir)
	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-program", "saved output")))
	output := filepath.Join(dir, "graded.json")

	cliOutput, err := executeGrade(t, specPath, "--task", "task-program", "--results", results, "--workspace", dir, "--output", output)
	contextPath, readErr := os.ReadFile(copyPath + ".path")
	require.NoError(t, readErr)
	contextDir := filepath.Dir(string(contextPath))
	t.Cleanup(func() {
		require.NoError(t, unlockUndeletableEntry(filepath.Join(contextDir, "locked")))
		require.NoError(t, os.RemoveAll(contextDir))
	})

	status, statusErr := os.ReadFile(copyPath + ".status")
	require.NoError(t, statusErr, "fixture setup report missing; CLI output: %s", cliOutput)
	require.Equal(t, "ready", string(status), "fixture setup failed; CLI output: %s", cliOutput)
	require.ErrorContains(t, err, "removing grading context directory "+contextDir)
	require.ErrorContains(t, err, "transcript data may remain")
	require.NoFileExists(t, output, "a cleanup failure must not produce a graded result")
	require.DirExists(t, contextDir, "residue remains at the reported path")
}

func TestGradeCommand_ProgramGraderContextSetupFailureIsObservable(t *testing.T) {
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "context-copy.json")
	require.NoError(t, os.Mkdir(copyPath, 0o700))
	t.Setenv(programContextCopyEnv, copyPath)
	t.Setenv(programContextLockEnv, "")
	specPath := writeProgramContextTask(t, dir)
	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-program", "saved output")))
	output := filepath.Join(dir, "graded.json")

	_, err := executeGrade(t, specPath, "--task", "task-program", "--results", results, "--workspace", dir, "--output", output)
	require.NoError(t, err, "a program's nonzero exit is a failed grade, not a CLI error")
	status, err := os.ReadFile(copyPath + ".status")
	require.NoError(t, err)
	require.Contains(t, string(status), "copying grading context:")
	require.Contains(t, string(status), copyPath)
	contextPath, err := os.ReadFile(copyPath + ".path")
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Dir(string(contextPath)))

	data, err := os.ReadFile(output)
	require.NoError(t, err)
	var graded models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &graded))
	require.Len(t, graded.TestOutcomes, 1)
	require.Len(t, graded.TestOutcomes[0].Runs, 1)
	result, ok := graded.TestOutcomes[0].Runs[0].Validations["context_check"]
	require.True(t, ok)
	require.False(t, result.Passed)
	require.Zero(t, result.Score)
	require.Contains(t, result.Feedback, "Program exited with error:")
	require.Contains(t, result.Feedback, string(status))
}

func TestGradeCommand_ProgramGraderReceivesSavedRunContext(t *testing.T) {
	exitCode := 0
	recorded := []models.CommandInvocation{{Command: "az", Args: []string{"account", "show"}, ExitCode: &exitCode}}

	for _, tc := range []struct {
		name     string
		commands []models.CommandInvocation
		want     string
	}{
		{name: "recorded invocations", commands: recorded},
		{name: "no recorded invocations", want: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			copyPath := filepath.Join(dir, "context-copy.json")
			t.Setenv(programContextCopyEnv, copyPath)
			specPath := writeProgramContextTask(t, dir)
			task := taskOutcome("task-program", "saved output")
			task.Runs[0].CommandInvocations = tc.commands
			results := gradeResultsFile(t, dir, outcomeWithTasks(task))

			_, err := executeGrade(t, specPath, "--task", "task-program", "--results", results, "--workspace", dir)
			require.NoError(t, err)

			data, err := os.ReadFile(copyPath)
			require.NoError(t, err)
			status, err := os.ReadFile(copyPath + ".status")
			require.NoError(t, err)
			require.Equal(t, "ready", string(status))
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.JSONEq(t, "1", string(fields["version"]))
			require.JSONEq(t, `"saved output"`, string(fields["output"]))
			require.JSONEq(t, "1000", string(fields["duration_ms"]))
			raw, ok := fields["command_invocations"]
			require.True(t, ok)
			if tc.want != "" {
				require.Equal(t, tc.want, string(raw))
				return
			}
			var got []models.CommandInvocation
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, recorded, got)
		})
	}
}
