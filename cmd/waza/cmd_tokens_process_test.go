package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokensCheckProcessExitCodes(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, ".waza.yaml"), []byte(`tokens:
  limits:
    defaults:
      "*.md": 1
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "big.md"), []byte("# This has more than one token\n"), 0o600))

	run := func(t *testing.T, args ...string) ([]byte, []byte, int) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = fixture
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "WAZA_NO_UPDATE_CHECK=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return stdout.Bytes(), stderr.Bytes(), 0
		}
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "stderr=%s", stderr.String())
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
	}

	t.Run("advisory over budget exits zero", func(t *testing.T) {
		stdout, stderr, code := run(t, "tokens", "check", "big.md", "--format", "json")
		require.Equal(t, 0, code)
		require.Empty(t, stderr)
		assertExceededTokenReport(t, stdout)
	})

	t.Run("strict over budget exits one", func(t *testing.T) {
		stdout, stderr, code := run(t, "tokens", "check", "big.md", "--strict", "--format", "json")
		require.Equal(t, 1, code)
		require.Empty(t, stdout)
		assertExceededTokenReport(t, stderr)
	})

	t.Run("invalid input exits two", func(t *testing.T) {
		stdout, stderr, code := run(t, "tokens", "check", filepath.Join("missing", "note.md"), "--format", "json")
		require.Equal(t, 2, code)
		require.Empty(t, stdout)
		require.NotEmpty(t, stderr)
	})
}

func assertExceededTokenReport(t *testing.T, data []byte) {
	t.Helper()
	var report struct {
		TotalFiles    int `json:"totalFiles"`
		ExceededCount int `json:"exceededCount"`
		Results       []struct {
			File     string `json:"file"`
			Exceeded bool   `json:"exceeded"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(data, &report), "%s", data)
	require.Equal(t, 1, report.TotalFiles)
	require.Equal(t, 1, report.ExceededCount)
	require.Len(t, report.Results, 1)
	require.Equal(t, "big.md", report.Results[0].File)
	require.True(t, report.Results[0].Exceeded)
}
