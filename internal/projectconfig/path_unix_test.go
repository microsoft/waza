//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package projectconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFindPathPresenceAndErrors(t *testing.T) {
	for _, kind := range []string{"dangling symlink", "fifo", "permission error", "not a directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".waza.yaml")
			directory := root
			switch kind {
			case "dangling symlink":
				require.NoError(t, os.Symlink(path+".missing", path))
			case "fifo":
				require.NoError(t, unix.Mkfifo(path, 0o600))
			case "permission error":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory access checks")
				}
				require.NoError(t, os.Chmod(root, 0o600))
				t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0o700)) })
			case "not a directory":
				directory = filepath.Join(root, "regular-file")
				require.NoError(t, os.WriteFile(directory, nil, 0o600))
			}
			found, err := FindPath(directory)
			if kind == "permission error" || kind == "not a directory" {
				require.Error(t, err)
				require.False(t, os.IsNotExist(err), "real I/O errors must not become an absent configuration")
				require.Empty(t, found)
				return
			}
			require.NoError(t, err)
			require.Equal(t, path, found)
		})
	}
}
