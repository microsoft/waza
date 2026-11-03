package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

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
