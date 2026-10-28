//go:build linux

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplacePrivateArtifactReconcilesCommittedResidue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	original := []byte(`{"outcome":"success"}`)
	replacement := []byte(`{"outcome":"publication_failed"}`)
	published, err := writePrivateSnapshotWithHooks(root, original, privateWriterHooks{
		suffix: ".runner.json",
		closeDirectory: func(dir *os.File) error {
			return errors.Join(dir.Close(), errors.New("injected final close failure"))
		},
	})
	require.Error(t, err)
	require.NotEmpty(t, published)

	require.NoError(t, ReplacePrivateArtifact(root, published, original, replacement))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	content, err := os.ReadFile(filepath.Join(root, entries[0].Name()))
	require.NoError(t, err)
	require.Equal(t, replacement, content)
}

func TestReplacePrivateArtifactDoesNotDeleteCompetingFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	original := []byte("successful terminal")
	published, err := writePrivateSnapshotWithHooks(root, original, privateWriterHooks{
		suffix: ".runner.json",
		closeDirectory: func(dir *os.File) error {
			return errors.Join(dir.Close(), errors.New("injected final close failure"))
		},
	})
	require.Error(t, err)
	require.NoError(t, os.Rename(published, published+".owned"))
	require.NoError(t, os.WriteFile(published, []byte("competing"), 0o600))

	err = ReplacePrivateArtifact(root, published, original, []byte("publication failed"))
	require.ErrorContains(t, err, "verified original")
	content, readErr := os.ReadFile(published)
	require.NoError(t, readErr)
	require.Equal(t, []byte("competing"), content)
}

func TestReplacePrivateArtifactRejectsReplacedParent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "private")
	original := []byte("successful terminal")
	published, err := writePrivateSnapshotWithHooks(root, original, privateWriterHooks{
		suffix: ".runner.json",
		closeDirectory: func(dir *os.File) error {
			return errors.Join(dir.Close(), errors.New("injected final close failure"))
		},
	})
	require.Error(t, err)
	require.NoError(t, os.Rename(root, root+".old"))
	require.NoError(t, os.Mkdir(root, 0o700))
	require.Error(t, ReplacePrivateArtifact(root, published, original, []byte("publication failed")))
	content, readErr := os.ReadFile(filepath.Join(root+".old", filepath.Base(published)))
	require.NoError(t, readErr)
	require.Equal(t, original, content)
}
