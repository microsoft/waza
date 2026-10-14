//go:build linux || darwin

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateWriterLateCloseRetainsResidualPublication(t *testing.T) {
	root := filepath.Join(workspaceTestRoot(t), "private")
	published, err := writePrivateSnapshotWithHooks(root, []byte("recorded"), privateWriterHooks{
		closeDirectory: func(dir *os.File) error {
			require.NoError(t, dir.Close())
			return errors.New("injected late close")
		},
	})
	require.ErrorContains(t, err, "close private evidence directory")
	require.NotEmpty(t, published)
	require.NotContains(t, err.Error(), root)
	content, err := os.ReadFile(published)
	require.NoError(t, err)
	require.Equal(t, "recorded", string(content))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "temporary link must be removed before final close")
}

func TestMaterializeRejectsSameInodeContentChanges(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "before verification", true: "after verification read"}[duringRead], func(t *testing.T) {
			snap := &Snapshot{WorkspaceFiles: []WorkspaceFile{{Path: "output.txt", Content: "original"}}}
			var root string
			mutate := func(dir, name string) {
				filename := filepath.Join(dir, name)
				before, err := os.Stat(filename)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filename, []byte("modified"), 0o600))
				require.NoError(t, os.Chtimes(filename, before.ModTime(), before.ModTime()))
				after, err := os.Stat(filename)
				require.NoError(t, err)
				require.True(t, os.SameFile(before, after))
			}

			hooks := materializeHooks{afterAdmission: func(dir string) { root = dir }}
			if duringRead {
				hooks.afterVerificationRead = mutate
			} else {
				hooks.beforePublication = func(dir string) { mutate(dir, "output.txt") }
			}
			workspace, err := materializeWithHooks(snap, []string{"output.txt"}, hooks)
			require.Error(t, err)
			require.Nil(t, workspace)
			require.Contains(t, err.Error(), "content changed")
			require.False(t, strings.Contains(err.Error(), "modified"))
			_, statErr := os.Stat(root)
			require.ErrorIs(t, statErr, os.ErrNotExist, "failed verification must clean owned restoration")
		})
	}
}

func TestMaterializeVerifiesEmptyAndChangedLengthContents(t *testing.T) {
	for _, tt := range []struct {
		name, original, replacement string
		changed                     bool
	}{
		{name: "empty"},
		{name: "LF and UTF-8", original: "line one\ncaf\u00e9\n"},
		{name: "empty growth", replacement: "x", changed: true},
		{name: "nonempty growth", original: "x", replacement: "xx", changed: true},
		{name: "nonempty shrink", original: "xx", replacement: "x", changed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snap := &Snapshot{WorkspaceFiles: []WorkspaceFile{{Path: "output.txt", Content: tt.original}}}
			var root string
			hooks := materializeHooks{afterAdmission: func(dir string) { root = dir }}
			if tt.changed {
				hooks.beforePublication = func(dir string) {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "output.txt"), []byte(tt.replacement), 0o600))
				}
			}
			workspace, err := materializeWithHooks(snap, []string{"output.txt"}, hooks)
			if tt.changed {
				require.ErrorContains(t, err, "content changed")
				require.Nil(t, workspace)
				_, statErr := os.Stat(root)
				require.ErrorIs(t, statErr, os.ErrNotExist)
			} else {
				require.NoError(t, err)
				data, err := os.ReadFile(filepath.Join(workspace.Path(), "output.txt"))
				require.NoError(t, err)
				require.Equal(t, tt.original, string(data))
				require.NoError(t, workspace.Close())
			}
		})
	}
}
