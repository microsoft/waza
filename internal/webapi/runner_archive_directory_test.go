package webapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerArchiveInvalidPrivateDirectoryRemainsDiagnostic(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	for _, name := range []string{".runner", "a.runner.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), encoded, 0o600))
	}
	page, err := readRunnerPage(t.Context(), dir, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Results, 2)
	require.Equal(t, ".runner", page.Results[0].Artifact)
	require.Contains(t, page.Results[0].Error, "directory")
	require.Empty(t, page.Results[0].Result, "a malformed storage directory is not an envelope")
	require.NotEmpty(t, page.Results[1].Result)
	require.Empty(t, page.Results[1].Error)
}
