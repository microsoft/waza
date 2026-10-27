package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerLocalUnsupportedCommandHasActionableDiagnostic(t *testing.T) {
	dir := isolatedCLITempDir(t)
	eval, err := os.ReadFile("../../internal/runnercontract/testdata/local/eval-1.0.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("../../internal/runnercontract/testdata/local/task.yaml")
	require.NoError(t, err)
	task = []byte(strings.Replace(string(task), "  prompt: hello", "  prompt_file: never-open-this.txt", 1))
	evalPath, taskPath := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "task.yaml")
	require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
	require.NoError(t, os.WriteFile(taskPath, task, 0o600))
	original := newPublicRunnerService
	t.Cleanup(func() { newPublicRunnerService = original })
	newPublicRunnerService = func() *runnercontract.Service {
		t.Fatal("adaptation must never construct an execution service")
		return nil
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"runner", "adapt", "--eval", evalPath, "--task", taskPath,
		"--run-key", "frozen-local", "--shard", "local", "--task-id", "selected", "--trial", "1", "--attempt", "1"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	err = cmd.Execute()
	require.ErrorContains(t, err, "prompt_file")
	require.ErrorContains(t, err, "waza run")
	require.ErrorContains(t, err, "waza runner adapt")
	require.Empty(t, output.Bytes(), "an unsupported mapping must not emit a runnable partial request")
	_, err = os.Stat(filepath.Join(dir, "never-open-this.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
