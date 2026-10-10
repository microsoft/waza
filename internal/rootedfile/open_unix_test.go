//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package rootedfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOpenUnixRequestsNonblockingDescriptor(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("regular"), 0600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	file, err := Open(root, "input")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	require.NoError(t, err)
	require.NotZero(t, flags&unix.O_NONBLOCK)
}

func TestOpenUnixFIFOIsRawAndDoesNotWaitForWriter(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fifo")
	require.NoError(t, unix.Mkfifo(path, 0600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	type result struct {
		file *os.File
		err  error
	}
	done := make(chan result, 1)
	go func() {
		file, err := Open(root, "fifo")
		done <- result{file, err}
	}()
	var opened result
	select {
	case opened = <-done:
	case <-time.After(time.Second):
		// Release only this FIFO if a regression introduced a blocking open.
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		opened = <-done
		require.NoError(t, unix.Close(fd))
		if opened.file != nil {
			require.NoError(t, opened.file.Close())
		}
		t.Fatal("raw FIFO open waited for a writer")
	}
	require.NoError(t, opened.err)
	require.NotNil(t, opened.file)
	t.Cleanup(func() { require.NoError(t, opened.file.Close()) })
	info, err := opened.file.Stat()
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe, "raw Open does not certify a regular file")
}
