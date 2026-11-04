package projectconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindPathAncestorLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".waza.yaml")
	require.NoError(t, os.WriteFile(path, []byte("invalid: [must not parse"), 0o600))
	directory := root
	for depth := 0; depth <= 10; depth++ {
		require.NoError(t, os.MkdirAll(directory, 0o700))
		found, err := FindPath(directory)
		if depth < 10 {
			require.NoError(t, err)
			require.Equal(t, path, found)
		} else {
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Empty(t, found)
			cfg, err := Load(directory)
			require.NoError(t, err)
			require.Empty(t, cfg.Dir, "the native loader has the same ancestor limit")
		}
		directory = filepath.Join(directory, "child")
	}
}

func TestFindPathRelativeAndDirectoryEntry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".waza.yaml")
	require.NoError(t, os.Mkdir(path, 0o700))
	t.Chdir(root)
	found, err := FindPath(".")
	require.NoError(t, err)
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	require.Equal(t, absolute, found)
}
