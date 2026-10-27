//go:build linux || darwin

package snapshot

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerArtifactExclusiveCollisionAndResidualFailure(t *testing.T) {
	t.Run("collision", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		var existing string
		name, err := writePrivateSnapshotWithHooks(root, []byte("new observation"), privateWriterHooks{
			suffix: ".runner.json",
			beforePublication: func() {
				entries, err := os.ReadDir(root)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				final := strings.TrimSuffix(strings.TrimPrefix(entries[0].Name(), "."), ".tmp")
				existing = filepath.Join(root, final)
				require.NoError(t, os.WriteFile(existing, []byte("existing observation"), 0o600))
			},
		})
		require.Error(t, err)
		require.Empty(t, name)
		content, err := os.ReadFile(existing)
		require.NoError(t, err)
		require.Equal(t, "existing observation", string(content))
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		require.Len(t, entries, 1, "failed exclusive publication must clean its staging file")
	})
	t.Run("cleanup-error", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		name, err := writePrivateSnapshotWithHooks(root, []byte("complete observation"), privateWriterHooks{
			suffix: ".runner.json",
			unlink: func(int, string, int) error { return io.ErrClosedPipe },
		})
		require.ErrorContains(t, err, "remove incomplete private evidence failed")
		require.NotEmpty(t, name, "verified residue must remain locatable")
		content, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		require.Equal(t, "complete observation", string(content))
	})
}
