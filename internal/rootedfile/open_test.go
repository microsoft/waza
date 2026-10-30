//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package rootedfile

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenRegularAndMissing(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("raw bytes"), 0600))
	file, err := Open(root, "input")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	info, err := file.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	data, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, "raw bytes", string(data))
	missing, err := Open(root, "missing")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, missing)
}

func TestOpenPathRegularAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(path, []byte("native path bytes"), 0o600))
	file, err := OpenPath(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	info, err := file.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	data, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, "native path bytes", string(data))
	missing, err := OpenPath(path + ".missing")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, missing)
}
