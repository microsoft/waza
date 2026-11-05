package assurance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadDocumentBoundsConfinementAndCancellation(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("1234"), 0o600))
	data, err := ReadDocument(t.Context(), root, "input", 4)
	require.NoError(t, err)
	require.Equal(t, []byte("1234"), data)
	for _, limit := range []int{0, -1, 3, maxSnapshotBytes + 1} {
		data, err := ReadDocument(t.Context(), root, "input", limit)
		require.Error(t, err)
		require.Nil(t, data)
	}
	for _, name := range []string{"missing", ".", "../outside"} {
		data, err := ReadDocument(t.Context(), root, name, 4)
		require.Error(t, err)
		require.Nil(t, data)
	}
	_, err = ReadDocument(t.Context(), nil, "input", 4)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, err = ReadDocument(ctx, root, "input", 4)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, data)
}
