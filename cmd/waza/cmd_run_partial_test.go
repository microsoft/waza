package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type cancelOnSecondExecutionEngine struct {
	*execution.MockEngine
	cancel context.CancelFunc
	calls  int
}

func (e *cancelOnSecondExecutionEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	if e.calls == 2 {
		e.cancel()
		return &execution.ExecutionResponse{FinalOutput: "partial output"}, context.Canceled
	}
	return &execution.ExecutionResponse{Success: true, FinalOutput: "completed"}, nil
}

func TestRunCommandOutputDirRemovesPartialAfterFinalWrite(t *testing.T) {
	resetRunGlobals()
	specPath := createTestSpec(t, "mock")
	dir := t.TempDir()

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	require.NoError(t, cmd.Execute())

	var finalFiles, partialFiles []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			if strings.Contains(path, string(filepath.Separator)+".partial"+string(filepath.Separator)) {
				partialFiles = append(partialFiles, path)
			} else {
				finalFiles = append(finalFiles, path)
			}
		}
		return nil
	}))
	require.NotEmpty(t, finalFiles)
	require.Empty(t, partialFiles)
}

func TestRunDiscoverModeOutputDirPublishesFinalResults(t *testing.T) {
	resetRunGlobals()
	root := t.TempDir()
	skillDir := filepath.Join(root, "discover-skill")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: discover-skill\ndescription: test\n---\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "tasks", "task.yaml"), []byte("id: task\nname: Task\ninputs:\n  prompt: hi\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "eval.yaml"), []byte(`
name: discover-eval
skill: discover-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`), 0o600))
	dir := t.TempDir()

	cmd := newRunCommand()
	cmd.SetArgs([]string{"--discover", root, "--output-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	var finalFiles, partialFiles []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+".partial"+string(filepath.Separator)) {
			partialFiles = append(partialFiles, path)
		} else {
			finalFiles = append(finalFiles, path)
		}
		return nil
	}))
	require.NotEmpty(t, finalFiles)
	require.Empty(t, partialFiles)
}

func TestRunCommandCancellationRetainsPartialOutput(t *testing.T) {
	resetRunGlobals()
	oldEngineFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldEngineFactory
		resetRunGlobals()
	})

	ctx, cancel := context.WithCancel(t.Context())
	engine := &cancelOnSecondExecutionEngine{
		MockEngine: execution.NewMockEngine("test-model"),
		cancel:     cancel,
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		return engine, nil
	}

	specPath := createTestSpec(t, "mock")
	specData, err := os.ReadFile(specPath)
	require.NoError(t, err)
	specData = []byte(strings.Replace(string(specData), "trials_per_task: 1", "trials_per_task: 3", 1))
	require.NoError(t, os.WriteFile(specPath, specData, 0o644))
	dir := t.TempDir()

	cmd := newRunCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{specPath, "--output-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	require.ErrorIs(t, cmd.Execute(), context.Canceled)
	require.Equal(t, 2, engine.calls)

	finalFiles, partialFiles := outcomeJSONFiles(t, dir)
	require.Empty(t, finalFiles)
	require.Len(t, partialFiles, 1)

	var partial models.EvaluationOutcome
	data, err := os.ReadFile(partialFiles[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &partial))
	require.Equal(t, true, partial.Metadata["incomplete"])
	require.Len(t, partial.TestOutcomes, 1)
	require.Len(t, partial.TestOutcomes[0].Runs, 2)
	require.Equal(t, models.StatusPassed, partial.TestOutcomes[0].Runs[0].Status)
	require.Equal(t, models.StatusError, partial.TestOutcomes[0].Runs[1].Status)
}

func TestRunCommandShutdownTimeoutRetainsPartialOutput(t *testing.T) {
	resetRunGlobals()
	oldEngineFactory := newRunEngine
	oldGrace := runEngineShutdownGrace
	oldTimeout := runEngineShutdownTimeout
	t.Cleanup(func() {
		newRunEngine = oldEngineFactory
		runEngineShutdownGrace = oldGrace
		runEngineShutdownTimeout = oldTimeout
		resetRunGlobals()
	})
	runEngineShutdownGrace = 5 * time.Millisecond
	runEngineShutdownTimeout = 5 * time.Millisecond

	engine := &persistentCancellationEngine{
		AgentEngine: execution.NewMockEngine("test-model"),
		forced:      make(chan struct{}),
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		return engine, nil
	}
	dir := t.TempDir()

	cmd := newRunCommand()
	cmd.SetArgs([]string{createTestSpec(t, "mock"), "--output-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	require.ErrorContains(t, cmd.Execute(), "engine shutdown exceeded maximum duration")
	select {
	case <-engine.forced:
	default:
		t.Fatal("timed-out shutdown did not force-stop the persistent engine")
	}

	finalFiles, partialFiles := outcomeJSONFiles(t, dir)
	require.Empty(t, finalFiles)
	require.Len(t, partialFiles, 1)
}

func TestSaveOutcomePreservesPreviousCheckpointWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	original := []byte(`{"schema_version":"preserved"}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	previousReplace := replaceOutcomeFileForSave
	t.Cleanup(func() { replaceOutcomeFileForSave = previousReplace })
	replaceOutcomeFileForSave = func(_, destination string) error {
		require.Equal(t, path, destination)
		return os.ErrPermission
	}

	replacement := &models.EvaluationOutcome{SchemaVersion: models.CurrentResultSchemaVersion}
	require.ErrorIs(t, saveOutcome(replacement, path), os.ErrPermission)

	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, original, got)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestAllocatePartialOutcomePathIsInvocationSpecific(t *testing.T) {
	resetRunGlobals()
	outputDir = t.TempDir()
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "partial"},
		SkillName:    "skill",
		Config:       models.Config{ModelID: "model"},
	}

	first, err := allocatePartialOutcomePath(spec)
	require.NoError(t, err)
	second, err := allocatePartialOutcomePath(spec)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Equal(t, filepath.Dir(first), filepath.Dir(second))
	require.Contains(t, first, filepath.Join(outputDir, ".partial"))
}

func TestSaveOutcomePublishesValidReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":"old"}`), 0o600))

	outcome := &models.EvaluationOutcome{SchemaVersion: models.CurrentResultSchemaVersion}
	require.NoError(t, saveOutcome(outcome, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &saved))
	require.Equal(t, models.CurrentResultSchemaVersion, saved.SchemaVersion)
}

func TestSaveOutcomePreservesDestinationPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":"old"}`), 0o600))
	before, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, saveOutcome(&models.EvaluationOutcome{SchemaVersion: models.CurrentResultSchemaVersion}, path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.Mode().Perm(), info.Mode().Perm())
}

func TestCreateOutcomeTempFileIsPrivateBeforePublication(t *testing.T) {
	dir := t.TempDir()
	publicControl, err := os.OpenFile(filepath.Join(dir, "public-control"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	require.NoError(t, err)
	publicInfo, err := publicControl.Stat()
	require.NoError(t, err)
	require.NoError(t, publicControl.Close())

	privateControl, err := os.OpenFile(filepath.Join(dir, "private-control"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	privateInfo, err := privateControl.Stat()
	require.NoError(t, err)
	require.NoError(t, privateControl.Close())

	file, publishMode, err := createOutcomeTempFile(filepath.Join(dir, "result.json"))
	require.NoError(t, err)
	tempPath := file.Name()
	t.Cleanup(func() { _ = os.Remove(tempPath) })
	info, err := file.Stat()
	require.NoError(t, err)
	require.NoError(t, file.Close())

	require.Equal(t, privateInfo.Mode().Perm(), info.Mode().Perm())
	require.Equal(t, publicInfo.Mode().Perm(), publishMode)
}

func outcomeJSONFiles(t *testing.T, dir string) ([]string, []string) {
	t.Helper()
	var finalFiles, partialFiles []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+".partial"+string(filepath.Separator)) {
			partialFiles = append(partialFiles, path)
		} else {
			finalFiles = append(finalFiles, path)
		}
		return nil
	}))
	return finalFiles, partialFiles
}
