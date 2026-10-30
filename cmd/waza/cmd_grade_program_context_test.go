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

// TestProgramGradingContextCopyHelper runs as a program grader and copies the
// grading context file so the parent test can inspect it.
func TestProgramGradingContextCopyHelper(t *testing.T) {
	dest := os.Getenv(programContextCopyEnv)
	if dest == "" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	contextPath := os.Getenv(graders.ProgramGradingContextEnv)
	data, err := os.ReadFile(contextPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := errors.Join(
		os.WriteFile(dest, data, 0o600),
		os.WriteFile(dest+".path", []byte(contextPath), 0o600),
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if os.Getenv(programContextLockEnv) != "" {
		// Leave an entry in the context directory that the grader's own user
		// cannot delete, so Waza's cleanup fails.
		if err := lockUndeletableEntry(filepath.Join(filepath.Dir(contextPath), "locked")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	fmt.Print("context copied")
	os.Exit(0)
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
	if !undeletableEntriesEnforced() {
		t.Skip("running as root: permissions cannot make cleanup fail")
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "context-copy.json")
	t.Setenv(programContextCopyEnv, copyPath)
	t.Setenv(programContextLockEnv, "1")
	specPath := writeProgramContextTask(t, dir)
	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-program", "saved output")))
	output := filepath.Join(dir, "graded.json")

	_, err := executeGrade(t, specPath, "--task", "task-program", "--results", results, "--workspace", dir, "--output", output)
	contextPath, readErr := os.ReadFile(copyPath + ".path")
	require.NoError(t, readErr)
	contextDir := filepath.Dir(string(contextPath))
	t.Cleanup(func() {
		require.NoError(t, unlockUndeletableEntry(filepath.Join(contextDir, "locked")))
		require.NoError(t, os.RemoveAll(contextDir))
	})

	require.ErrorContains(t, err, "removing grading context directory "+contextDir)
	require.ErrorContains(t, err, "transcript data may remain")
	require.NoFileExists(t, output, "a cleanup failure must not produce a graded result")
	require.DirExists(t, contextDir, "residue remains at the reported path")
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
