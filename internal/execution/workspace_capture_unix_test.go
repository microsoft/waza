//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package execution

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWalkWorkspaceCaptureRejectsDirectoryReplacedWithFIFOAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "queued")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trigger"), []byte("ready"), 0o600))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- walkWorkspaceCapture(root, 10, func(_, _ string) error {
			// The root's single ReadDir batch already contains the directory
			// candidate, but it has not been opened for traversal yet.
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syscall.Mkfifo(target, 0o600); err != nil {
				return err
			}
			cancel()
			return nil
		})
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, `workspace directory "queued" changed during capture`)
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	case <-time.After(time.Second):
		// Release a regressed blocking reader before failing, so the test
		// never leaks a goroutine or leaves its workspace open.
		writer, err := os.OpenFile(target, os.O_RDWR|syscall.O_NONBLOCK, 0)
		require.NoError(t, err)
		defer func() { require.NoError(t, writer.Close()) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("workspace capture did not unblock after opening the FIFO writer")
		}
		t.Fatal("workspace capture blocked on a replaced directory despite cancellation")
	}
}

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
