package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixCopilotPackageInGoFile(t *testing.T) {
	for _, name := range []string{"zcopilot_darwin_arm64.go", "zcopilot_inprocess_darwin_arm64.go"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			require.NoError(t, os.WriteFile(path, []byte("package main\nvar value=1\n"), 0600))
			require.NoError(t, fixCopilotPackageInGoFile(path))
			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "package embedded\n\nvar value = 1\n", string(contents))
		})
	}
	t.Run("missing file", func(t *testing.T) {
		require.ErrorContains(t, fixCopilotPackageInGoFile(filepath.Join(t.TempDir(), "missing.go")), "failed to read")
	})
	t.Run("invalid Go", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.go")
		require.NoError(t, os.WriteFile(path, []byte("package main\nfunc {\n"), 0600))
		require.ErrorContains(t, fixCopilotPackageInGoFile(path), "failed to gofmt")
	})
}
