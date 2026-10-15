package nativesnapshot

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

type readContext struct {
	context.Context
	calls   int
	onCheck func(int) error
}

func (c *readContext) Err() error {
	c.calls++
	return c.onCheck(c.calls)
}

func reader(t *testing.T, ctx context.Context) (*capturedFiles, string) {
	t.Helper()
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	total := 0
	return &capturedFiles{ctx: ctx, root: root, base: base, data: map[string][]byte{},
		info: map[string]os.FileInfo{}, dirs: map[string][]fs.DirEntry{},
		roles: map[string]map[string]bool{}, total: &total, role: "input"}, base
}

func TestSourceReadBoundsCancellationAndChangingRead(t *testing.T) {
	t.Run("source_size", func(t *testing.T) {
		files, base := reader(t, context.Background())
		file, err := os.Create(filepath.Join(base, "large"))
		require.NoError(t, err)
		require.NoError(t, file.Truncate(maxSourceBytes+1))
		require.NoError(t, file.Close())
		_, err = files.ReadFile(filepath.Join(base, "large"))
		require.ErrorContains(t, err, "source limit")
	})
	t.Run("executable_size", func(t *testing.T) {
		files, base := reader(t, context.Background())
		files.role = "executable"
		file, err := os.Create(filepath.Join(base, "large"))
		require.NoError(t, err)
		require.NoError(t, file.Truncate(maxExecutableBytes+1))
		require.NoError(t, file.Close())
		_, err = files.ReadFile(filepath.Join(base, "large"))
		require.ErrorContains(t, err, "source limit")
	})
	t.Run("total", func(t *testing.T) {
		files, base := reader(t, context.Background())
		*files.total = maxTotalBytes
		write(t, base, "input", "x")
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.ErrorContains(t, err, "byte limit")
	})
	t.Run("cancel_between_chunks", func(t *testing.T) {
		ctx := &readContext{Context: context.Background(), onCheck: func(call int) error {
			if call >= 5 {
				return context.Canceled
			}
			return nil
		}}
		files, base := reader(t, ctx)
		write(t, base, "input", string(make([]byte, 128<<10)))
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, files.data)
	})
	t.Run("cancel_at_last_pre_open_check", func(t *testing.T) {
		ctx := &readContext{Context: context.Background(), onCheck: func(call int) error {
			if call >= 3 {
				return context.Canceled
			}
			return nil
		}}
		files, base := reader(t, ctx)
		write(t, base, "input", "original")
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, files.data)
	})
	t.Run("cancel_after_bytes_before_descriptor_recheck", func(t *testing.T) {
		ctx := &readContext{Context: context.Background(), onCheck: func(call int) error {
			if call >= 6 {
				return context.Canceled
			}
			return nil
		}}
		files, base := reader(t, ctx)
		write(t, base, "input", "original")
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, files.data)
	})
	t.Run("mutation_after_read", func(t *testing.T) {
		ctx := &readContext{Context: context.Background()}
		files, base := reader(t, ctx)
		write(t, base, "input", "original")
		ctx.onCheck = func(call int) error {
			if call == 6 {
				write(t, base, "input", "different length")
			}
			return nil
		}
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.ErrorContains(t, err, "changed during read")
		require.Empty(t, files.data)
	})
	t.Run("directory_is_not_source", func(t *testing.T) {
		files, base := reader(t, context.Background())
		require.NoError(t, os.Mkdir(filepath.Join(base, "directory"), 0700))
		_, err := files.ReadFile(filepath.Join(base, "directory"))
		require.ErrorContains(t, err, "regular file")
	})
	t.Run("frozen_reader_no_reopen", func(t *testing.T) {
		files, base := reader(t, context.Background())
		write(t, base, "input", "captured")
		_, err := files.ReadFile(filepath.Join(base, "input"))
		require.NoError(t, err)
		files.frozen = true
		require.NoError(t, os.Remove(filepath.Join(base, "input")))
		data, err := files.ReadFile(filepath.Join(base, "input"))
		require.NoError(t, err)
		require.Equal(t, "captured", string(data))
		_, err = files.ReadFile(filepath.Join(base, "new"))
		require.ErrorContains(t, err, "uncaptured")
	})
	t.Run("directory_entries", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("native directory discovery is unsupported on Windows; covered by the platform negative test")
		}
		files, base := reader(t, context.Background())
		files.entries = maxEntries
		write(t, base, "input", "x")
		_, err := files.ReadDir(".")
		require.ErrorContains(t, err, "directory entries")
	})
}
