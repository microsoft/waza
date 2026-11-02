//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package execution

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCaptureWorkspaceFiles_SkipsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "result.txt"), []byte("ready"), 0o644))
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "stream"), 0o600))

	type result struct {
		files map[string][]byte
		err   error
	}
	done := make(chan result, 1)
	go func() {
		files, err := captureWorkspaceFiles(dir, nil)
		done <- result{files: files, err: err}
	}()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, map[string][]byte{"result.txt": []byte("ready")}, got.files)
	case <-time.After(time.Second):
		t.Fatal("workspace capture blocked on FIFO")
	}
}

func TestCaptureWorkspaceFiles_RejectsPreservedFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "stream"), 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := captureWorkspaceFiles(dir, []string{"stream"})
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorContains(t, err, `preserved path "stream" is not a regular file`)
	case <-time.After(time.Second):
		t.Fatal("workspace capture blocked on preserved FIFO")
	}
}
