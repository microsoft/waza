//go:build linux || darwin

package assurance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestQualificationSecureRootedFilesRejectFIFOAndSymlinks(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.NoError(t, root.WriteFile("regular.json", []byte("é\n"), 0o600))
	require.NoError(t, unix.Mkfifo(filepath.Join(directory, "fifo.json"), 0o600))
	require.NoError(t, root.Symlink("regular.json", "link.json"))
	require.NoError(t, root.Mkdir("directory.json", 0o700))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, name := range []string{"fifo.json", "link.json", "directory.json", "../outside", "missing"} {
		_, err := qualificationSecureRead(ctx, root, name, 1024)
		require.Error(t, err, name)
	}
	data, err := qualificationSecureRead(ctx, root, "regular.json", 3)
	require.NoError(t, err)
	require.Equal(t, []byte("é\n"), data)
	_, err = qualificationSecureRead(ctx, root, "regular.json", 2)
	require.Error(t, err, "byte boundary +1")
	io := qualificationOSJournalIO{}
	_, err = io.OpenDirectDirectory(ctx, root, "fifo.json")
	require.Error(t, err)
	_, err = io.OpenDirectDirectory(ctx, root, "link.json")
	require.Error(t, err)
	require.NoError(t, root.Remove("regular.json"))
	require.NoError(t, root.Symlink("fifo.json", "regular.json"))
	_, err = qualificationSecureRead(ctx, root, "regular.json", 1024)
	require.Error(t, err, "replacement special path never blocks")
	require.NoError(t, io.SyncDirectory(ctx, root))
	readCtx := context.WithValue(ctx, qualificationReadLockKey{}, struct{}{})
	_, err = io.Lock(readCtx, root)
	require.Error(t, err, "read-only lock cannot create registry state")
	_, err = root.Lstat("registry.lock")
	require.ErrorIs(t, err, os.ErrNotExist)
}
