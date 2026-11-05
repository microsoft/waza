//go:build !windows

package graders

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func assertPrivateContextFile(t *testing.T, path string) {
	t.Helper()
	dir, err := os.Lstat(filepath.Dir(path))
	require.NoError(t, err)
	require.True(t, dir.IsDir())
	require.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
	file, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, file.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), file.Mode().Perm())
}
