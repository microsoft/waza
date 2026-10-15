//go:build !darwin && !linux

package nativetask

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTapeDurableCreationUnsupportedBeforeArtifacts(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "sentinel"), []byte("unchanged"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "reserved"), 0o700))
	parent, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	for _, name := range []string{"collection", "reserved", "", "../outside"} {
		t.Run(name, func(t *testing.T) {
			tape, err := CreateTape(context.Background(), parent, name)
			require.Nil(t, tape)
			require.ErrorContains(t, err, "unsupported on "+runtime.GOOS)
			require.ErrorContains(t, err, "use Linux or macOS")
			require.ErrorContains(t, err, "reading supplied tapes remains available")
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Len(t, entries, 2, "no reservation or tape artifact may precede platform rejection")
			require.Equal(t, "reserved", entries[0].Name())
			require.Equal(t, "sentinel", entries[1].Name())
			data, err := parent.ReadFile("sentinel")
			require.NoError(t, err)
			require.Equal(t, []byte("unchanged"), data)
			reserved, err := os.ReadDir(filepath.Join(directory, "reserved"))
			require.NoError(t, err)
			require.Empty(t, reserved)
		})
	}
	tape, err := CreateTape(context.Background(), nil, "collection")
	require.Nil(t, tape)
	require.ErrorContains(t, err, "unsupported on "+runtime.GOOS)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tape, err = CreateTape(ctx, parent, "collection")
	require.Nil(t, tape)
	require.ErrorIs(t, err, context.Canceled)
	_, err = os.Stat(filepath.Join(directory, "collection"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
