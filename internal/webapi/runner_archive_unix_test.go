//go:build linux || darwin

package webapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerArchiveNonregularEntriesDoNotBlockValidSiblings(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.runner.json"), encoded, 0o600))
	require.NoError(t, unix.Mkfifo(filepath.Join(dir, "b.runner.json"), 0o600))
	require.NoError(t, os.Symlink("a.runner.json", filepath.Join(dir, "c.runner.json")))
	done := make(chan runnerArchivePage, 1)
	go func() {
		page, err := readRunnerPage(t.Context(), dir, "", 20)
		if err == nil {
			done <- page
		}
	}()
	select {
	case page := <-done:
		require.Len(t, page.Results, 3)
		require.NotEmpty(t, page.Results[0].Result)
		require.Empty(t, page.Results[0].Error)
		require.NotEmpty(t, page.Results[1].Error)
		require.NotEmpty(t, page.Results[2].Error)
	case <-time.After(5 * time.Second):
		t.Fatal("nonregular archive entries blocked listing")
	}
}

func TestRunnerArchiveCursorPreservesRawFilenameBytes(t *testing.T) {
	for _, name := range []string{"a.runner.json", ".runner/unit.runner.json", "\xff.runner.json", "\"\\\n.runner.json"} {
		decoded, err := parseArchiveCursor(archiveCursor(name))
		require.NoError(t, err)
		require.Equal(t, name, decoded)
	}
}
