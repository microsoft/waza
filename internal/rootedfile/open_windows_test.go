//go:build windows

package rootedfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenWindowsStableRegularDirectoryAndMissing(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("regular"), 0600))
	for _, name := range []string{"input", ".", "missing"} {
		t.Run(name, func(t *testing.T) {
			file, err := Open(root, name)
			if name != "input" {
				require.Error(t, err)
				require.Nil(t, file)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, file.Close()) })
			info, err := file.Stat()
			require.NoError(t, err)
			require.True(t, info.Mode().IsRegular())
		})
	}
}
