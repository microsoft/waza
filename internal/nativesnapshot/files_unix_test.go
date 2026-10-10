//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package nativesnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSourceReadRejectsFIFOReplacementAtLastPreOpenCheck(t *testing.T) {
	ctx := &readContext{Context: context.Background()}
	files, base := reader(t, ctx)
	write(t, base, "input", "original regular file")
	path := filepath.Join(base, "input")
	replaced := false
	ctx.onCheck = func(call int) error {
		if call != 3 { // relative check, rooted Lstat, then final pre-open check
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("replacing original input: %w", err)
		}
		if err := unix.Mkfifo(path, 0600); err != nil {
			return fmt.Errorf("creating replacement FIFO: %w", err)
		}
		replaced = true
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := files.ReadFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		require.True(t, replaced, "replacement must follow the regular-file precheck")
		require.ErrorContains(t, err, "input changed before read")
		require.Empty(t, files.data)
	case <-time.After(time.Second):
		// No writer exists on the passing path. Unblock this specific FIFO only
		// to clean up a regression before failing, rather than orphaning a read.
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		<-done
		require.NoError(t, unix.Close(fd))
		t.Fatal("resolver waited for a writer after a raced FIFO replacement")
	}
}

func TestSourceReadDirRejectsFIFOReplacementAtLastPreOpenCheck(t *testing.T) {
	ctx := &readContext{Context: context.Background()}
	files, base := reader(t, ctx)
	path := filepath.Join(base, "directory")
	require.NoError(t, os.Mkdir(path, 0700))
	replaced := false
	ctx.onCheck = func(call int) error {
		if call != 3 { // ReadDir entry, rooted Lstat, then final pre-open check
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("replacing original directory: %w", err)
		}
		if err := unix.Mkfifo(path, 0600); err != nil {
			return fmt.Errorf("creating replacement FIFO: %w", err)
		}
		replaced = true
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := files.ReadDir("directory")
		done <- err
	}()
	select {
	case err := <-done:
		require.True(t, replaced, "replacement must follow the directory precheck")
		require.ErrorContains(t, err, "directory changed before capture")
		require.Empty(t, files.dirs)
	case <-time.After(time.Second):
		// Unblock only this replacement FIFO on the failing path.
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		<-done
		require.NoError(t, unix.Close(fd))
		t.Fatal("directory discovery waited for a replacement FIFO writer")
	}
}
