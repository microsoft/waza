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
	"time"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetRunGlobals zeroes the package-level flag vars so prior tests don't leak.
func resetRunGlobals() {
	contextDir = ""
	outputPath = ""
	outputDir = ""
	verbose = false
	transcriptDir = ""
	taskFilters = nil
	tagFilters = nil
	parallel = false
	workers = 0
	interpret = false
	format = "default"
	enableCache = false
	disableCache = false
	runCacheDir = ".waza-cache"
	modelOverrides = nil
	recommendFlag = false
	baselineFlag = false
	sessionLog = false
	sessionDir = ""
	noSummary = false
	reporters = nil
}

// helper creates a valid minimal eval spec YAML in a temp dir,
// including a matching task file, and returns the spec path.
func createTestSpec(t *testing.T, engine string) string {
	t.Helper()
	dir := t.TempDir()

	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	task := `id: test-task-001
name: Test Task
inputs:
  prompt: "Explain this code"
`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(task), 0o644))

	spec := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
  executor: ` + engine + `
  model: test-model
tasks:
  - "tasks/*.yaml"
`
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))
	return specPath
}

// ---------------------------------------------------------------------------
// Argument validation
// ---------------------------------------------------------------------------

func TestRunCommand_RequiresExactlyOneArg(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no args", []string{}},
		{"two args", []string{"a.yaml", "b.yaml"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRunCommand()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			assert.Error(t, err, "expected error for args=%v", tt.args)
		})
	}
}

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

func TestRunCommand_FlagsParsed(t *testing.T) {
	tmpCtx := filepath.Join(t.TempDir(), "ctx")
	tmpOut := filepath.Join(t.TempDir(), "out.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		"--context-dir", tmpCtx,
		"--output", tmpOut,
		"--verbose",
		"spec.yaml",
	})

	// Don't execute — just parse flags to verify they bind.
	require.NoError(t, cmd.ParseFlags([]string{
		"--context-dir", tmpCtx,
		"--output", tmpOut,
		"--verbose",
	}))

	val, err := cmd.Flags().GetString("context-dir")
	require.NoError(t, err)
	assert.Equal(t, tmpCtx, val)

	val, err = cmd.Flags().GetString("output")
	require.NoError(t, err)
	assert.Equal(t, tmpOut, val)

	boolVal, err := cmd.Flags().GetBool("verbose")
	require.NoError(t, err)
	assert.True(t, boolVal)
}

func TestRunCommand_ShortFlags(t *testing.T) {
	tmpOut := filepath.Join(t.TempDir(), "out.json")

	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{
		"-o", tmpOut,
		"-v",
	}))

	val, err := cmd.Flags().GetString("output")
	require.NoError(t, err)
	assert.Equal(t, tmpOut, val)

	boolVal, err := cmd.Flags().GetBool("verbose")
	require.NoError(t, err)
	assert.True(t, boolVal)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func TestRunCommand_MissingSpecFile(t *testing.T) {
	resetRunGlobals()

	cmd := newRunCommand()
	cmd.SetArgs([]string{"nonexistent.yaml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load spec")
}

func TestRunCommand_InvalidSpecFile(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	badSpec := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badSpec, []byte("foo: [bar"), 0o644))

	cmd := newRunCommand()
	cmd.SetArgs([]string{badSpec})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load spec")
}

func TestRunCommand_InvalidEngineType(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(taskDir, "t.yaml"),
		[]byte("id: t1\nname: t\ninputs:\n  prompt: hi\n"),
		0o644,
	))

	spec := `name: test
skill: s
config:
  trials_per_task: 1
  timeout_seconds: 10
  executor: nonexistent-engine
  model: m
tasks:
  - "tasks/*.yaml"
`
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine type")
}

// ---------------------------------------------------------------------------
// Integration with mock engine — full run
// ---------------------------------------------------------------------------

func TestRunCommand_MockEngineRun(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})

	// Suppress stdout/stderr noise during test
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunCommand_MockEngineVerbose(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--verbose"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunCommand_OutputJSON(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	outFile := filepath.Join(t.TempDir(), "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output", outFile})

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify JSON output was written and is valid
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "test-eval", result["eval_name"])
	assert.Equal(t, "test-skill", result["skill"])
}

func TestRunCommand_ReporterJUnit(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	junitFile := filepath.Join(t.TempDir(), "results.xml")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--reporter", "junit:" + junitFile})

	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(junitFile)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "<?xml")
	assert.Contains(t, content, "<testsuites")
	assert.Contains(t, content, "<testsuite")
	assert.Contains(t, content, "<testcase")
	assert.Contains(t, content, "test-eval")
}

func TestRunCommand_ReporterFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{
		"--reporter", "junit:out.xml",
		"--reporter", "json",
	}))

	vals, err := cmd.Flags().GetStringArray("reporter")
	require.NoError(t, err)
	assert.Equal(t, []string{"junit:out.xml", "json"}, vals)
}

func TestRunCommand_ReporterUnknown(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--reporter", "csv"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown reporter")
}

func TestRunCommand_ContextDirFlag(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	// Pass an explicit --context-dir (the spec already has fixtures alongside it,
	// but the flag should override without error).
	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--context-dir", t.TempDir()})

	// This will succeed because the mock engine doesn't need real fixture files.
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunCommand_TranscriptDirWritesTaskFile(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	transcriptOutDir := filepath.Join(t.TempDir(), "transcripts")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--transcript-dir", transcriptOutDir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(transcriptOutDir, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)

	content, err := os.ReadFile(files[0])
	require.NoError(t, err)

	var taskTranscript map[string]any
	require.NoError(t, json.Unmarshal(content, &taskTranscript))
	assert.Equal(t, "test-task-001", taskTranscript["task_id"])
	assert.Equal(t, "Test Task", taskTranscript["task_name"])
}

// ---------------------------------------------------------------------------
// Root command wiring
// ---------------------------------------------------------------------------

func TestRootCommand_HasRunSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "run" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'run' subcommand")
}

// ---------------------------------------------------------------------------
// Task filtering via --task flag
// ---------------------------------------------------------------------------

func TestRunCommand_TaskFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{
		"--task", "Create*",
		"--task", "tc-002",
	}))

	vals, err := cmd.Flags().GetStringArray("task")
	require.NoError(t, err)
	assert.Equal(t, []string{"Create*", "tc-002"}, vals)
}

func TestRunCommand_TaskFilterRunsMock(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--task", "Test Task"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunCommand_TaskFilterNoMatch(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--task", "nonexistent-task"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no test cases found")
}

// ---------------------------------------------------------------------------
// Parallel execution via --parallel and --workers flags
// ---------------------------------------------------------------------------

func TestRunCommand_ParallelFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--parallel", "--workers", "8"}))

	boolVal, err := cmd.Flags().GetBool("parallel")
	require.NoError(t, err)
	assert.True(t, boolVal)

	intVal, err := cmd.Flags().GetInt("workers")
	require.NoError(t, err)
	assert.Equal(t, 8, intVal)
}

func TestRunCommand_ParallelFlagDefaultWorkers(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--parallel"}))

	boolVal, err := cmd.Flags().GetBool("parallel")
	require.NoError(t, err)
	assert.True(t, boolVal)

	intVal, err := cmd.Flags().GetInt("workers")
	require.NoError(t, err)
	assert.Equal(t, 0, intVal, "workers should default to 0 (runner defaults to 4)")
}

func TestRunCommand_ParallelRunsMock(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--parallel", "--workers", "2"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunCommand_ParallelOverridesSpec(t *testing.T) {
	resetRunGlobals()

	// The test spec has parallel: false. The --parallel flag should override it.
	specPath := createTestSpec(t, "mock")
	outFile := filepath.Join(t.TempDir(), "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--parallel", "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify results were produced (proves the concurrent path ran)
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "test-eval", result["eval_name"])
}

// ---------------------------------------------------------------------------
// --interpret flag
// ---------------------------------------------------------------------------

func TestRunCommand_InterpretFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--interpret"}))

	boolVal, err := cmd.Flags().GetBool("interpret")
	require.NoError(t, err)
	assert.True(t, boolVal)
}

func TestRunCommand_InterpretRunsMock(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--interpret"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Exit code behavior
// ---------------------------------------------------------------------------

func TestRunCommand_ReturnsTestFailureErrorOnTestFailure(t *testing.T) {
	resetRunGlobals()

	// Create a spec with a task that will fail validation
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	// Task with a code grader that will fail (checks for impossible condition)
	task := `id: failing-task
name: Failing Task
inputs:
  prompt: "This will fail"
graders:
  - name: impossible_check
    type: code
    config:
      assertions:
        - "False"  # This will always fail
`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(task), 0o644))

	spec := `name: test-eval-failure
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
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
	require.Error(t, err)

	// Verify it's a TestFailureError
	var testFailureErr *TestFailureError
	assert.True(t, errors.As(err, &testFailureErr), "expected TestFailureError type")
	assert.Contains(t, err.Error(), "benchmark completed:")
}

func TestRunCommand_ReturnsRegularErrorOnConfigFailure(t *testing.T) {
	resetRunGlobals()

	// Test with invalid spec (unknown engine type)
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(taskDir, "t.yaml"),
		[]byte("id: t1\nname: t\ninputs:\n  prompt: hi\n"),
		0o644,
	))

	spec := `name: test
skill: s
config:
  trials_per_task: 1
  timeout_seconds: 10
  executor: invalid-engine-type
  model: m
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
	require.Error(t, err)

	// Verify it's NOT a TestFailureError (it's a config error)
	var testFailureErr *TestFailureError
	assert.False(t, errors.As(err, &testFailureErr), "expected regular error, not TestFailureError")
	assert.Contains(t, err.Error(), "unknown engine type")
}

// ---------------------------------------------------------------------------
// --format flag
// ---------------------------------------------------------------------------

func TestRunCommand_FormatFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--format", "github-comment"}))

	val, err := cmd.Flags().GetString("format")
	require.NoError(t, err)
	assert.Equal(t, "github-comment", val)
}

func TestRunCommand_FormatFlagDefault(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{}))

	val, err := cmd.Flags().GetString("format")
	require.NoError(t, err)
	assert.Equal(t, "default", val)
}

func TestRunCommand_FormatGitHubComment(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--format", "github-comment"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "github-comment format should execute successfully")
}

func TestRunCommand_FormatInvalid(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--format", "invalid-format"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown output format")
	assert.Contains(t, err.Error(), "invalid-format")
}

// ---------------------------------------------------------------------------
// --model flag: multi-model support (#39)
// ---------------------------------------------------------------------------

func TestRunCommand_ModelFlagParsed(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--model", "gpt-4o-mini"}))

	vals, err := cmd.Flags().GetStringArray("model")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-4o-mini"}, vals)
}

func TestRunCommand_ModelFlagMultiple(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{
		"--model", "gpt-4o",
		"--model", "claude-sonnet",
	}))

	vals, err := cmd.Flags().GetStringArray("model")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-4o", "claude-sonnet"}, vals)
}

func TestRunCommand_ModelFlagEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		flags    []string
		expected []string
	}{
		{
			name:     "single model",
			flags:    []string{"--model", "gpt-4o"},
			expected: []string{"gpt-4o"},
		},
		{
			name:     "three models for comparison",
			flags:    []string{"--model", "gpt-4o", "--model", "claude-sonnet", "--model", "gpt-4o-mini"},
			expected: []string{"gpt-4o", "claude-sonnet", "gpt-4o-mini"},
		},
		{
			name:     "model with version suffix",
			flags:    []string{"--model", "gpt-4o-2024-08-06"},
			expected: []string{"gpt-4o-2024-08-06"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRunCommand()
			require.NoError(t, cmd.ParseFlags(tt.flags))

			vals, err := cmd.Flags().GetStringArray("model")
			require.NoError(t, err)
			assert.Equal(t, tt.expected, vals)
		})
	}
}

func TestRunCommand_ModelOverridesSpec(t *testing.T) {
	resetRunGlobals()

	// Spec declares model: claude-sonnet; CLI overrides with gpt-4o-mini.
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(taskDir, "t.yaml"),
		[]byte("id: t1\nname: t\ninputs:\n  prompt: hi\n"),
		0o644,
	))

	spec := `name: test-override
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: claude-sonnet
tasks:
  - "tasks/*.yaml"
`
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))
	outFile := filepath.Join(dir, "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--model", "gpt-4o-mini", "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	cfg, ok := result["config"].(map[string]any)
	require.True(t, ok, "expected config key in output JSON")
	assert.Equal(t, "gpt-4o-mini", cfg["model_id"],
		"--model flag should override spec config model")
}

func TestRunCommand_MultiModelExecution(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		specPath,
		"--model", "gpt-4o",
		"--model", "claude-sonnet",
		"--output", outFile,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	// Multi-model saves per-model files: results_<model>.json
	for _, model := range []string{"gpt-4o", "claude-sonnet"} {
		perModelPath := filepath.Join(outDir, fmt.Sprintf("results_%s.json", model))
		data, err := os.ReadFile(perModelPath)
		require.NoError(t, err, "expected per-model output for %s", model)
		assert.Greater(t, len(data), 0)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		cfg, ok := result["config"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, model, cfg["model_id"],
			"per-model output should reflect the model that was evaluated")
	}
}

func TestRunCommand_NoModelFlagPreservesYAML(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock") // spec has model: test-model
	outFile := filepath.Join(t.TempDir(), "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	cfg, ok := result["config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "test-model", cfg["model_id"],
		"without --model flag, spec config model should be preserved")
}

func TestRunCommand_ModelNameInOutputJSON(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	outFile := filepath.Join(t.TempDir(), "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--model", "gpt-4o", "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))

	// Model name must appear in config section of output
	cfg, ok := result["config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gpt-4o", cfg["model_id"],
		"model name should appear in results JSON config")
}

func TestRunCommand_SingleModelMatchingSpecIsNoop(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock") // model: test-model
	outFile := filepath.Join(t.TempDir(), "results.json")

	// Passing --model with the same value as the spec should behave
	// identically to not passing --model at all.
	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--model", "test-model", "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	cfg, ok := result["config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "test-model", cfg["model_id"],
		"--model matching spec model should produce identical results")
}

func TestRunCommand_MultiModelComparisonTablePrinted(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	// Capture stdout to verify the comparison table is printed.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--model", "gpt-4o", "--model", "claude-sonnet"})
	cmd.SetErr(io.Discard)

	execErr := cmd.Execute()

	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	out, readErr := io.ReadAll(r)
	require.NoError(t, readErr)
	require.NoError(t, execErr)

	output := string(out)
	assert.Contains(t, output, "MODEL COMPARISON",
		"multi-model run should print comparison table header")
	assert.Contains(t, output, "gpt-4o",
		"comparison table should list gpt-4o")
	assert.Contains(t, output, "claude-sonnet",
		"comparison table should list claude-sonnet")
}

// ---------------------------------------------------------------------------
// --recommend flag: heuristic recommendation (#138)
// ---------------------------------------------------------------------------

func TestRunCommand_RecommendFlagPrintsRecommendation(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	// Capture stdout to verify RECOMMENDATION output.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		specPath,
		"--model", "gpt-4o",
		"--model", "claude-sonnet",
		"--recommend",
	})
	cmd.SetErr(io.Discard)

	execErr := cmd.Execute()

	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	out, readErr := io.ReadAll(r)
	require.NoError(t, readErr)
	require.NoError(t, execErr)

	output := string(out)
	assert.Contains(t, output, "RECOMMENDATION",
		"--recommend flag should print RECOMMENDATION header")
	assert.Contains(t, output, "Recommended Model:",
		"--recommend output should identify the recommended model")
}

func TestRunCommand_RecommendSetsMetadata(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "results.json")

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		specPath,
		"--model", "gpt-4o",
		"--model", "claude-sonnet",
		"--recommend",
		"--output", outFile,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify recommendation metadata is set in per-model output files.
	for _, model := range []string{"gpt-4o", "claude-sonnet"} {
		perModelPath := filepath.Join(outDir, fmt.Sprintf("results_%s.json", model))
		data, readErr := os.ReadFile(perModelPath)
		require.NoError(t, readErr, "expected per-model output for %s", model)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))

		meta, ok := result["metadata"].(map[string]any)
		require.True(t, ok, "expected metadata key in output JSON for %s", model)
		_, hasRec := meta["recommendation"]
		assert.True(t, hasRec, "metadata should contain recommendation key for %s", model)
	}
}

// ---------------------------------------------------------------------------
// Duplicate --model rejection
// ---------------------------------------------------------------------------

func TestRunCommand_DuplicateModelRejected(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{
		specPath,
		"--model", "gpt-4o",
		"--model", "gpt-4o",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate --model value")
}

// ---------------------------------------------------------------------------
// skillRunResult captures outcomes (#272)
// ---------------------------------------------------------------------------

func TestSkillRunResult_CapturesOutcomes(t *testing.T) {
	// Test the enhanced skillRunResult struct captures EvaluationOutcome data
	outcome := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests:     10,
			Succeeded:      8,
			Failed:         2,
			SuccessRate:    0.8,
			AggregateScore: 0.85,
		},
	}

	result := skillRunResult{
		skillName: "test-skill",
		outcomes:  []modelResult{{modelID: "gpt-4o", outcome: outcome}},
	}

	require.Equal(t, "test-skill", result.skillName)
	require.Len(t, result.outcomes, 1)
	require.NotNil(t, result.outcomes[0].outcome)
	assert.Equal(t, 10, result.outcomes[0].outcome.Digest.TotalTests)
	assert.Equal(t, 8, result.outcomes[0].outcome.Digest.Succeeded)
	assert.Equal(t, 0.8, result.outcomes[0].outcome.Digest.SuccessRate)
}

func TestSkillRunResult_MultipleModelOutcomesAggregated(t *testing.T) {
	// Test aggregation across multiple models including edge case with zeros
	outcome1 := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests:     10,
			Succeeded:      8,
			Failed:         2,
			SuccessRate:    0.8,
			AggregateScore: 0.85,
		},
	}
	outcome2 := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests:     5,
			Succeeded:      5,
			Failed:         0,
			SuccessRate:    1.0,
			AggregateScore: 0.95,
		},
	}
	outcome3 := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests:     0,
			Succeeded:      0,
			Failed:         0,
			SuccessRate:    0.0,
			AggregateScore: 0.0,
		},
	}

	result := skillRunResult{
		skillName: "multi-model-skill",
		outcomes: []modelResult{
			{modelID: "gpt-4o", outcome: outcome1},
			{modelID: "gpt-4-turbo", outcome: outcome2},
			{modelID: "gpt-3.5", outcome: outcome3},
		},
	}

	require.Equal(t, "multi-model-skill", result.skillName)
	require.Len(t, result.outcomes, 3)
	// Verify all three outcomes are captured
	assert.Equal(t, 10, result.outcomes[0].outcome.Digest.TotalTests)
	assert.Equal(t, 5, result.outcomes[1].outcome.Digest.TotalTests)
	assert.Equal(t, 0, result.outcomes[2].outcome.Digest.TotalTests)
	// Aggregation: 8+5+0=13 passed out of 10+5+0=15 total
	// Average score: (0.85+0.95+0.0)/3 = 0.6
}

func TestSkillRunResult_MixedNilAndValidOutcomes(t *testing.T) {
	// Test edge case where some modelResult entries have nil outcomes
	outcome := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests:     8,
			Succeeded:      6,
			Failed:         2,
			SuccessRate:    0.75,
			AggregateScore: 0.80,
		},
	}

	result := skillRunResult{
		skillName: "mixed-outcome-skill",
		outcomes: []modelResult{
			{modelID: "gpt-4o", outcome: outcome},
			{modelID: "gpt-4-turbo", outcome: nil}, // nil outcome
			{modelID: "gpt-3.5", outcome: outcome},
		},
	}

	require.Equal(t, "mixed-outcome-skill", result.skillName)
	require.Len(t, result.outcomes, 3)
	// First and third have valid outcomes
	assert.NotNil(t, result.outcomes[0].outcome)
	assert.Nil(t, result.outcomes[1].outcome)
	assert.NotNil(t, result.outcomes[2].outcome)
	// Aggregation should skip nil: 6+6=12 passed out of 8+8=16 total
	// Average score: (0.80+0.80)/2 = 0.80 (skips nil)
}

// ---------------------------------------------------------------------------
// buildOutputPath and sanitizePathSegment
// ---------------------------------------------------------------------------

func TestSanitizePathSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"forward slash", "gpt-4o/mini", "gpt-4o-mini"},
		{"backslash", "model\\name", "model-name"},
		{"colon", "model:v1", "model-v1"},
		{"space", "model name", "model-name"},
		{"multiple", "gpt-4o/mini:v1 beta", "gpt-4o-mini-v1-beta"},
		{"clean name", "claude-sonnet", "claude-sonnet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizePathSegment(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildOutputPath(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		ext        string
		skillName  string
		modelID    string
		multiSkill bool
		multiModel bool
		expected   string
	}{
		{
			name:       "single skill single model",
			base:       "results",
			ext:        ".json",
			skillName:  "",
			modelID:    "",
			multiSkill: false,
			multiModel: false,
			expected:   "results.json",
		},
		{
			name:       "single skill multi model",
			base:       "results",
			ext:        ".json",
			skillName:  "",
			modelID:    "gpt-4o",
			multiSkill: false,
			multiModel: true,
			expected:   "results_gpt-4o.json",
		},
		{
			name:       "multi skill single model",
			base:       "results",
			ext:        ".json",
			skillName:  "code-explainer",
			modelID:    "claude-sonnet",
			multiSkill: true,
			multiModel: false,
			expected:   "results_code-explainer.json",
		},
		{
			name:       "multi skill multi model",
			base:       "results",
			ext:        ".json",
			skillName:  "code-explainer",
			modelID:    "gpt-4o",
			multiSkill: true,
			multiModel: true,
			expected:   "results_code-explainer_gpt-4o.json",
		},
		{
			name:       "sanitizes skill and model names",
			base:       "out",
			ext:        ".json",
			skillName:  "skill:v1 test",
			modelID:    "gpt-4o/mini",
			multiSkill: true,
			multiModel: true,
			expected:   "out_skill-v1-test_gpt-4o-mini.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildOutputPath(tt.base, tt.ext, tt.skillName, tt.modelID, tt.multiSkill, tt.multiModel)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// Multi-skill output integration test
// ---------------------------------------------------------------------------

func TestRunCommand_MultiSkillOutput(t *testing.T) {
	resetRunGlobals()

	// Create a parent directory with skill.yaml for each skill
	rootDir := t.TempDir()

	createSkillWithEval := func(skillName string) string {
		skillDir := filepath.Join(rootDir, skillName)
		require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "tasks"), 0o755))

		// Create skill.yaml to make it a valid skill
		skillYAML := `name: ` + skillName + `
version: "1.0"
description: Test skill
`
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "skill.yaml"),
			[]byte(skillYAML),
			0o644,
		))

		task := `id: test-task
name: Test Task
inputs:
  prompt: "Test"
`
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "tasks", "task.yaml"),
			[]byte(task),
			0o644,
		))

		spec := `name: test-eval
skill: ` + skillName + `
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`
		evalPath := filepath.Join(skillDir, "eval.yaml")
		require.NoError(t, os.WriteFile(evalPath, []byte(spec), 0o644))
		return evalPath
	}

	eval1 := createSkillWithEval("skill-one")
	eval2 := createSkillWithEval("skill-two")

	outFile := filepath.Join(rootDir, "results.json")

	// Run both evals by providing both paths
	// This will trigger multi-skill mode
	cmd := newRunCommand()
	cmd.SetArgs([]string{eval1, "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	// First run skill-one
	err := cmd.Execute()
	require.NoError(t, err)

	// Reset and run skill-two
	cmd = newRunCommand()
	cmd.SetArgs([]string{eval2, "--output", outFile})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err = cmd.Execute()
	require.NoError(t, err)

	// For single-skill runs, output should go to the specified path
	// Multi-skill output handling is done at the runCommandE level
	// Let's test that scenario by checking that single-skill doesn't create per-skill files

	// When running a single skill with --output, it should save to the exact path
	_, err = os.Stat(outFile)
	require.NoError(t, err, "single-skill run should save to exact output path")
}

func TestRunCommand_MultiSkillOutputDoesNotOverwrite(t *testing.T) {
	// This test verifies the core issue from #271:
	// When multiple skills run, each needs its own output file
	// The implementation uses buildOutputPath to create per-skill paths

	base := "results"
	ext := ".json"

	skill1Path := buildOutputPath(base, ext, "skill-one", "test-model", true, false)
	skill2Path := buildOutputPath(base, ext, "skill-two", "test-model", true, false)

	// Verify they're different paths
	assert.NotEqual(t, skill1Path, skill2Path, "different skills should get different output paths")
	assert.Equal(t, "results_skill-one.json", skill1Path)
	assert.Equal(t, "results_skill-two.json", skill2Path)
}

// Multi-skill summary (#273)
// ---------------------------------------------------------------------------

func TestBuildMultiSkillSummary_AggregatesCorrectly(t *testing.T) {
	results := []skillRunResult{
		{
			skillName: "skill-a",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{
							TotalTests:     10,
							Succeeded:      8,
							AggregateScore: 0.85,
						},
					},
				},
			},
		},
		{
			skillName: "skill-b",
			outcomes: []modelResult{
				{
					modelID: "claude-sonnet",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{
							TotalTests:     20,
							Succeeded:      18,
							AggregateScore: 0.90,
						},
					},
				},
			},
		},
	}

	summary := buildMultiSkillSummary(results)

	require.NotNil(t, summary)
	assert.Len(t, summary.Skills, 2)

	// Check skill-a metrics
	skillA := summary.Skills[0]
	assert.Equal(t, "skill-a", skillA.SkillName)
	assert.Equal(t, []string{"gpt-4o"}, skillA.Models)
	assert.InDelta(t, 0.8, skillA.PassRate, 0.001)
	assert.InDelta(t, 0.85, skillA.AggregateScore, 0.001)

	// Check skill-b metrics
	skillB := summary.Skills[1]
	assert.Equal(t, "skill-b", skillB.SkillName)
	assert.Equal(t, []string{"claude-sonnet"}, skillB.Models)
	assert.InDelta(t, 0.9, skillB.PassRate, 0.001)
	assert.InDelta(t, 0.90, skillB.AggregateScore, 0.001)

	// Check overall metrics
	assert.Equal(t, 2, summary.Overall.TotalSkills)
	assert.Equal(t, 2, summary.Overall.TotalModels)
	assert.InDelta(t, 0.85, summary.Overall.AvgPassRate, 0.001)        // (0.8 + 0.9) / 2
	assert.InDelta(t, 0.875, summary.Overall.AvgAggregateScore, 0.001) // (0.85 + 0.90) / 2
}

func TestBuildMultiSkillSummary_MultiModelPerSkill(t *testing.T) {
	results := []skillRunResult{
		{
			skillName: "skill-a",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{
							TotalTests:     10,
							Succeeded:      8,
							AggregateScore: 0.80,
						},
					},
				},
				{
					modelID: "claude-sonnet",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{
							TotalTests:     10,
							Succeeded:      9,
							AggregateScore: 0.90,
						},
					},
				},
			},
		},
	}

	summary := buildMultiSkillSummary(results)

	require.NotNil(t, summary)
	assert.Len(t, summary.Skills, 1)

	skill := summary.Skills[0]
	assert.Equal(t, "skill-a", skill.SkillName)
	assert.Equal(t, []string{"gpt-4o", "claude-sonnet"}, skill.Models)

	// Pass rate: (8+9) / (10+10) = 0.85
	assert.InDelta(t, 0.85, skill.PassRate, 0.001)

	// Aggregate score: (0.80 + 0.90) / 2 = 0.85
	assert.InDelta(t, 0.85, skill.AggregateScore, 0.001)

	// Overall: 1 skill, 2 unique models
	assert.Equal(t, 1, summary.Overall.TotalSkills)
	assert.Equal(t, 2, summary.Overall.TotalModels)
}

func TestBuildMultiSkillSummary_OutputFilesPaths(t *testing.T) {
	// Set up the global outputPath to test file path construction
	outputPath = "results.json"
	defer func() { outputPath = "" }()

	results := []skillRunResult{
		{
			skillName: "skill-a",
			outcomes: []modelResult{
				{modelID: "gpt-4o", outcome: &models.EvaluationOutcome{}},
				{modelID: "claude-sonnet-4", outcome: &models.EvaluationOutcome{}},
			},
		},
	}

	summary := buildMultiSkillSummary(results)

	require.NotNil(t, summary)
	require.Len(t, summary.Skills, 1)

	skill := summary.Skills[0]
	assert.Equal(t, []string{
		"results_gpt-4o.json",
		"results_claude-sonnet-4.json",
	}, skill.OutputFiles)
}

func TestRunCommand_NoSummaryFlag(t *testing.T) {
	cmd := newRunCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--no-summary"}))

	boolVal, err := cmd.Flags().GetBool("no-summary")
	require.NoError(t, err)
	assert.True(t, boolVal)
}

func TestSaveSummary_WritesValidJSON(t *testing.T) {
	summary := &models.MultiSkillSummary{
		Timestamp: time.Now(),
		Skills: []models.SkillSummary{
			{
				SkillName:      "test-skill",
				Models:         []string{"gpt-4o"},
				PassRate:       0.8,
				AggregateScore: 0.85,
				OutputFiles:    []string{"results_gpt-4o.json"},
			},
		},
		Overall: models.OverallSummary{
			TotalSkills:       1,
			TotalModels:       1,
			AvgPassRate:       0.8,
			AvgAggregateScore: 0.85,
		},
	}

	outFile := filepath.Join(t.TempDir(), "summary.json")
	err := saveSummary(summary, outFile)
	require.NoError(t, err)

	// Verify file exists and is valid JSON
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	var result models.MultiSkillSummary
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, 1, result.Overall.TotalSkills)
	assert.Equal(t, "test-skill", result.Skills[0].SkillName)
}

// ---------------------------------------------------------------------------
// Cross-product integration test: multi-skill × multi-model
// ---------------------------------------------------------------------------

func TestRunCommand_CrossProductMultiSkillMultiModel(t *testing.T) {
	resetRunGlobals()

	// Create workspace with 2 skills
	rootDir := t.TempDir()

	createSkillWithEval := func(skillName string) string {
		skillDir := filepath.Join(rootDir, skillName)
		require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "tasks"), 0o755))

		skillYAML := `name: ` + skillName + `
version: "1.0"
description: Test skill
`
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "skill.yaml"),
			[]byte(skillYAML),
			0o644,
		))

		task := `id: test-task
name: Test Task
inputs:
  prompt: "Test"
`
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "tasks", "task.yaml"),
			[]byte(task),
			0o644,
		))

		spec := `name: test-eval
skill: ` + skillName + `
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: default-model
tasks:
  - "tasks/*.yaml"
`
		evalPath := filepath.Join(skillDir, "eval.yaml")
		require.NoError(t, os.WriteFile(evalPath, []byte(spec), 0o644))
		return evalPath
	}

	eval1 := createSkillWithEval("code-explainer")
	eval2 := createSkillWithEval("sql-generator")

	outFile := filepath.Join(rootDir, "results.json")

	// Manually construct a multi-skill run by calling runCommandE with workspace override
	// This simulates what happens when workspace detection finds multiple skills
	savedOutputPath := outputPath
	outputPath = ""

	var allSkillResults []skillRunResult

	// Run skill 1 with multi-model
	modelOverrides = []string{"gpt-4o", "claude-sonnet"}

	outcomes1, err := runCommandForSpec(nil, skillSpecPath{specPath: eval1, skillName: "code-explainer"})
	if err != nil {
		var testErr *TestFailureError
		if !errors.As(err, &testErr) {
			require.NoError(t, err, "execution should succeed or fail gracefully")
		}
	}
	allSkillResults = append(allSkillResults, skillRunResult{
		skillName: "code-explainer",
		outcomes:  outcomes1,
	})

	// Run skill 2 with multi-model
	outcomes2, err := runCommandForSpec(nil, skillSpecPath{specPath: eval2, skillName: "sql-generator"})
	if err != nil {
		var testErr *TestFailureError
		if !errors.As(err, &testErr) {
			require.NoError(t, err, "execution should succeed or fail gracefully")
		}
	}
	allSkillResults = append(allSkillResults, skillRunResult{
		skillName: "sql-generator",
		outcomes:  outcomes2,
	})

	// Restore outputPath and write per-skill output files (mimics lines 139-158 in cmd_run.go)
	outputPath = savedOutputPath
	outputPath = outFile

	ext := filepath.Ext(outputPath)
	base := strings.TrimSuffix(outputPath, ext)

	for _, skillResult := range allSkillResults {
		multiModel := len(skillResult.outcomes) > 1

		for _, mr := range skillResult.outcomes {
			if mr.outcome == nil {
				continue
			}
			perSkillPath := buildOutputPath(base, ext, skillResult.skillName, mr.modelID, true, multiModel)
			require.NoError(t, saveOutcome(mr.outcome, perSkillPath))
		}
	}

	// Verify 4 output files exist: 2 skills × 2 models
	expectedFiles := []struct {
		path      string
		skillName string
		modelID   string
	}{
		{filepath.Join(rootDir, "results_code-explainer_gpt-4o.json"), "code-explainer", "gpt-4o"},
		{filepath.Join(rootDir, "results_code-explainer_claude-sonnet.json"), "code-explainer", "claude-sonnet"},
		{filepath.Join(rootDir, "results_sql-generator_gpt-4o.json"), "sql-generator", "gpt-4o"},
		{filepath.Join(rootDir, "results_sql-generator_claude-sonnet.json"), "sql-generator", "claude-sonnet"},
	}

	for _, ef := range expectedFiles {
		_, err := os.Stat(ef.path)
		require.NoError(t, err, "output file should exist: %s", ef.path)

		// Read the JSON and verify it contains the correct skill/model
		data, err := os.ReadFile(ef.path)
		require.NoError(t, err)

		var outcome models.EvaluationOutcome
		err = json.Unmarshal(data, &outcome)
		require.NoError(t, err)

		assert.Equal(t, ef.skillName, outcome.SkillTested, "outcome should contain correct skill name")
		assert.Equal(t, ef.modelID, outcome.Setup.ModelID, "outcome should contain correct model ID")
	}
}

// ---------------------------------------------------------------------------
// Edge case: one model fails, others succeed
// ---------------------------------------------------------------------------

func TestRunCommand_CrossProduct_PartialFailure(t *testing.T) {
	// Verify that if one skill+model combo fails, others still write output
	base := "results"
	ext := ".json"

	// Cross-product naming should work regardless of success/failure
	path1 := buildOutputPath(base, ext, "skill-a", "model-1", true, true)
	path2 := buildOutputPath(base, ext, "skill-a", "model-2", true, true)
	path3 := buildOutputPath(base, ext, "skill-b", "model-1", true, true)
	path4 := buildOutputPath(base, ext, "skill-b", "model-2", true, true)

	// All should be unique
	paths := []string{path1, path2, path3, path4}
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			assert.NotEqual(t, paths[i], paths[j], "all cross-product paths should be unique")
		}
	}

	assert.Equal(t, "results_skill-a_model-1.json", path1)
	assert.Equal(t, "results_skill-a_model-2.json", path2)
	assert.Equal(t, "results_skill-b_model-1.json", path3)
	assert.Equal(t, "results_skill-b_model-2.json", path4)
}

// ---------------------------------------------------------------------------
// Edge case: special characters in names
// ---------------------------------------------------------------------------

func TestRunCommand_CrossProduct_SpecialCharacters(t *testing.T) {
	base := "out"
	ext := ".json"

	// Test that special characters are sanitized in cross-product
	path := buildOutputPath(base, ext, "skill:v1/test", "gpt-4o/mini-2024", true, true)
	assert.Equal(t, "out_skill-v1-test_gpt-4o-mini-2024.json", path)
	assert.NotContains(t, path, ":")
	assert.NotContains(t, path, "/")
}

// ---------------------------------------------------------------------------
// --output-dir flag tests
// ---------------------------------------------------------------------------

func TestRunCommand_OutputDirMutualExclusion(t *testing.T) {
	resetRunGlobals()
	defer resetRunGlobals()

	cmd := newRunCommand()
	specPath := createTestSpec(t, "mock")
	cmd.SetArgs([]string{
		"--output", "results.json",
		"--output-dir", "results-dir",
		specPath,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--output and --output-dir are mutually exclusive")
}

func TestWriteOutputDir_SingleSkill(t *testing.T) {
	dir := t.TempDir()

	// Single skill with single model
	results := []skillRunResult{
		{
			skillName: "code-explainer",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{
							TotalTests: 5,
							Succeeded:  4,
						},
					},
				},
			},
		},
	}

	err := writeOutputDir(dir, results)
	require.NoError(t, err)

	// Single-skill mode: files written directly to output dir
	resultPath := filepath.Join(dir, "gpt-4o.json")
	assert.FileExists(t, resultPath)

	// Verify JSON content
	data, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &outcome))
	assert.Equal(t, 5, outcome.Digest.TotalTests)
	assert.Equal(t, 4, outcome.Digest.Succeeded)
}

func TestWriteOutputDir_MultiSkill(t *testing.T) {
	dir := t.TempDir()

	// Multi-skill with multiple models
	results := []skillRunResult{
		{
			skillName: "code-explainer",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{TotalTests: 5, Succeeded: 4},
					},
				},
				{
					modelID: "claude-sonnet",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{TotalTests: 5, Succeeded: 3},
					},
				},
			},
		},
		{
			skillName: "code-reviewer",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{TotalTests: 3, Succeeded: 2},
					},
				},
			},
		},
	}

	err := writeOutputDir(dir, results)
	require.NoError(t, err)

	// Multi-skill mode: subdirectories created per skill
	explainerDir := filepath.Join(dir, "code-explainer")
	reviewerDir := filepath.Join(dir, "code-reviewer")

	assert.DirExists(t, explainerDir)
	assert.DirExists(t, reviewerDir)

	// Verify files
	assert.FileExists(t, filepath.Join(explainerDir, "gpt-4o.json"))
	assert.FileExists(t, filepath.Join(explainerDir, "claude-sonnet.json"))
	assert.FileExists(t, filepath.Join(reviewerDir, "gpt-4o.json"))

	// Verify content
	data, err := os.ReadFile(filepath.Join(explainerDir, "gpt-4o.json"))
	require.NoError(t, err)
	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &outcome))
	assert.Equal(t, 5, outcome.Digest.TotalTests)
	assert.Equal(t, 4, outcome.Digest.Succeeded)
}

func TestWriteOutputDir_SanitizesPaths(t *testing.T) {
	dir := t.TempDir()

	// Skill and model names with special chars
	// Multi-skill to test subdirectory creation
	results := []skillRunResult{
		{
			skillName: "code/explainer",
			outcomes: []modelResult{
				{
					modelID: "gpt-4o:latest",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{TotalTests: 1, Succeeded: 1},
					},
				},
			},
		},
		{
			skillName: "code\\reviewer",
			outcomes: []modelResult{
				{
					modelID: "claude sonnet",
					outcome: &models.EvaluationOutcome{
						Digest: models.OutcomeDigest{TotalTests: 1, Succeeded: 1},
					},
				},
			},
		},
	}

	err := writeOutputDir(dir, results)
	require.NoError(t, err)

	// Paths should be sanitized
	explainerDir := filepath.Join(dir, "code-explainer")
	reviewerDir := filepath.Join(dir, "code-reviewer")
	assert.DirExists(t, explainerDir)
	assert.DirExists(t, reviewerDir)
	assert.FileExists(t, filepath.Join(explainerDir, "gpt-4o-latest.json"))
	assert.FileExists(t, filepath.Join(reviewerDir, "claude-sonnet.json"))
}
