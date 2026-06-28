package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateCommandNoOpForCurrentMajor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte("schemaVersion: \"1.0\"\n"), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.NoError(t, err)
	require.Contains(t, out.String(), "already compatible with schemaVersion 1.0")
}

func TestMigrateCommandRejectsIncompatibleMajor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":"2.0"}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.Error(t, err)
	require.Contains(t, err.Error(), `waza migrate <file>`)
	require.Empty(t, out.String())
}

func TestMigrateCommandDefaultsMissingSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"eval_id":"run-1"}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.NoError(t, err)
	require.Contains(t, out.String(), "already compatible with schemaVersion 1.0")
}

func TestMigrateCommandMissingFile(t *testing.T) {
	var out bytes.Buffer
	err := runMigrate(&out, filepath.Join(t.TempDir(), "missing.json"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "reading")
}
