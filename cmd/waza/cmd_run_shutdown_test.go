package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Engine Shutdown lifecycle in runSingleModel (#153)
//
// These tests verify that engine.Shutdown() is called in every exit path of
// runSingleModel — success, test failure, invalid format, and benchmark error.
// The mock engine's Shutdown is a no-op, so we validate indirectly by checking
// that the function completes without panic or resource leak.
//
// If Linus adds an engine factory or SpyEngine injection point, these tests
// should be upgraded to assert spy.WasCalled() directly.
// ---------------------------------------------------------------------------

func TestRunSingleModel_ShutdownOnSuccess(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "mock engine run should succeed and shutdown cleanly")
}

func TestRunSingleModel_ShutdownOnTestFailure(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	// Task with a grader that will always fail
	task := `id: shutdown-fail-task
name: Shutdown Fail Task
inputs:
  prompt: "This will fail"
graders:
  - name: always_fail
    type: code
    config:
      assertions:
        - "False"
`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(task), 0o644))

	spec := `name: shutdown-test-failure
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	// The error is expected (test failure), but Shutdown must still have run.
	require.Error(t, err, "benchmark with failing tests should return error")
	assert.Contains(t, err.Error(), "benchmark completed:")
}

func TestRunSingleModel_ShutdownOnInvalidFormat(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--format", "bogus-format"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	// Shutdown must still have run despite the format error after benchmark execution.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown output format")
}

func TestRunSingleModel_ShutdownOnUnknownEngine(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(taskDir, "t.yaml"),
		[]byte("id: t1\nname: t\ninputs:\n  prompt: hi\n"),
		0o644,
	))

	spec := `name: test-unknown-engine
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 10
  executor: nonexistent-engine
  model: test-model
tasks:
  - "tasks/*.yaml"
`
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	// Engine creation fails before Shutdown — this tests early-return path.
	// No engine was created, so no Shutdown to call. This must NOT panic.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine type")
}

func TestRunSingleModel_ShutdownWithMultipleModels(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		specPath,
		"--model", "model-a",
		"--model", "model-b",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	// Each model gets its own engine instance in the loop.
	// Shutdown must be called for each engine, not just the last one.
	assert.NoError(t, err, "multi-model run should shutdown each engine cleanly")
}

func TestRunSingleModel_ShutdownWithOutputWrite(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	outFile := filepath.Join(t.TempDir(), "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify output was written (proves Shutdown didn't interfere with output)
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0, "output file should be written before shutdown")
}

func TestRunSingleModel_ShutdownWithGitHubCommentFormat(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--format", "github-comment"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "github-comment format should shutdown cleanly")
}

func TestRunSingleModel_ShutdownWithCacheEnabled(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	cacheDir := filepath.Join(t.TempDir(), "cache")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--cache", "--cache-dir", cacheDir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "cached run should shutdown cleanly")
}

func TestRunSingleModel_ShutdownWithVerbose(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--verbose"})

	err := cmd.Execute()
	assert.NoError(t, err, "verbose run should shutdown cleanly")
}

// ---------------------------------------------------------------------------
// --keep-workspace flag integration tests (#123)
// ---------------------------------------------------------------------------

func TestRunSingleModel_KeepWorkspaceFlag(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	// Capture stderr to check for "Workspace preserved:" message
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--keep-workspace"})
	cmd.SetOut(io.Discard)

	err := cmd.Execute()

	_ = w.Close()
	var buf [8192]byte
	n, _ := r.Read(buf[:])
	os.Stderr = oldStderr

	assert.NoError(t, err, "run with --keep-workspace should succeed")
	assert.Contains(t, string(buf[:n]), "Workspace preserved:",
		"stderr should contain workspace path when --keep-workspace is set")
}

func TestRunSingleModel_KeepWorkspaceVerbose(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--keep-workspace", "--verbose"})

	err := cmd.Execute()
	assert.NoError(t, err, "verbose run with --keep-workspace should succeed")
}

func TestRunSingleModel_DefaultCleansWorkspace(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	// Capture stderr — should NOT contain "Workspace preserved:"
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)

	err := cmd.Execute()

	_ = w.Close()
	var buf [8192]byte
	n, _ := r.Read(buf[:])
	os.Stderr = oldStderr

	assert.NoError(t, err, "default run should succeed")
	assert.NotContains(t, string(buf[:n]), "Workspace preserved:",
		"stderr should NOT mention workspace preservation by default")
}
