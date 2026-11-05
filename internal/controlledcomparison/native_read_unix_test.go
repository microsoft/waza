//go:build darwin || linux

package controlledcomparison

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/microsoft/waza/internal/nativesnapshot"
	"github.com/microsoft/waza/internal/rootedfile"
)

func TestNativeArtifactRejectsFIFOReplacementBeforeOpen(t *testing.T) {
	directory := t.TempDir()
	name := "native-capture.json"
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte("{}"), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	called := false
	raw, err := nativeArtifactWithOpener(t.Context(), root, name, 512<<20, func(root *os.Root, name string) (*os.File, error) {
		called = true
		require.NoError(t, root.Rename(name, "original.json"))
		require.NoError(t, unix.Mkfifo(filepath.Join(directory, name), 0o600))
		return rootedfile.Open(root, name)
	})
	require.True(t, called, "replacement must occur after the initial regular-file check")
	require.ErrorContains(t, err, "changed")
	require.Nil(t, raw)
	info, err := root.Lstat(name)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe, "reader must not remove a competing replacement")
	original, err := root.ReadFile("original.json")
	require.NoError(t, err)
	require.Equal(t, "{}", string(original))
}

func TestNativeArtifactPublicWireBoundBeforeOpen(t *testing.T) {
	directory := t.TempDir()
	name := "native-capture.json"
	file, err := os.Create(filepath.Join(directory, name))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(nativesnapshot.CollectionWireBytes+1))
	require.NoError(t, file.Close())
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	called := false
	raw, err := nativeArtifactWithOpener(t.Context(), root, name, nativesnapshot.CollectionWireBytes, func(root *os.Root, name string) (*os.File, error) {
		called = true
		return rootedfile.Open(root, name)
	})
	require.ErrorContains(t, err, "limit")
	require.Nil(t, raw)
	require.False(t, called, "known oversize wire must fail before opening or allocating its contents")
}
