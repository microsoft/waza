package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunCommandOutputDirRemovesPartialAfterFinalWrite(t *testing.T) {
	resetRunGlobals()
	specPath := createTestSpec(t, "mock")
	dir := t.TempDir()

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	require.NoError(t, cmd.Execute())

	var resultFiles []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			resultFiles = append(resultFiles, path)
		}
		return nil
	}))
	require.Len(t, resultFiles, 1)
	require.NotContains(t, resultFiles[0], string(filepath.Separator)+".partial"+string(filepath.Separator))
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
