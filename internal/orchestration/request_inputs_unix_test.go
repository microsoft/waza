//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package orchestration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type lstatRequestFiles struct{ nativeRequestFiles }

func (lstatRequestFiles) Stat(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func TestRequestInputsActualSymlinkAndSpecialKinds(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	link := filepath.Join(base, "link")
	fifo := filepath.Join(base, "fifo")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0600))
	require.NoError(t, os.Symlink("file", link))
	require.NoError(t, syscall.Mkfifo(fifo, 0600))
	adapter := AdaptRequestFiles(lstatRequestFiles{})
	for _, row := range []struct {
		path string
		kind RequestInputKind
	}{{link, RequestInputSymlink}, {fifo, RequestInputOther}} {
		kind, err := adapter.Kind(row.path)
		require.NoError(t, err)
		require.Equal(t, row.kind, kind)
	}
	// Native Stat follows links just as before; the adapter does not Lstat them.
	kind, err := AdaptRequestFiles(nativeRequestFiles{}).Kind(link)
	require.NoError(t, err)
	require.Equal(t, RequestInputRegular, kind)
	got := map[string]RequestInputKind{}
	err = adapter.Walk(base, func(path string, kind RequestInputKind) error {
		got[path] = kind
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, map[string]RequestInputKind{
		base: RequestInputDirectory, file: RequestInputRegular,
		link: RequestInputSymlink, fifo: RequestInputOther,
	}, got)
}
