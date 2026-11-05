//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package assurance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestReadDocumentRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "fifo")
	require.NoError(t, unix.Mkfifo(name, 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	done := make(chan error, 1)
	go func() {
		_, err := ReadDocument(t.Context(), root, "fifo", 1024)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "regular file")
	case <-time.After(time.Second):
		fd, err := unix.Open(name, unix.O_RDWR|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		<-done
		require.NoError(t, unix.Close(fd))
		t.Fatal("document reader waited for a FIFO writer")
	}
}
