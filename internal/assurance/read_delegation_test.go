//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package assurance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/rootedfile"
	"github.com/stretchr/testify/require"
)

func TestReferenceOpenerSharedDelegationParity(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "input"), []byte("original"), 0600))
	for _, name := range []string{"input", ".", "missing", "../outside"} {
		t.Run(name, func(t *testing.T) {
			delegated, delegatedErr := openReferenceDocument(root, name)
			if delegated != nil {
				t.Cleanup(func() { require.NoError(t, delegated.Close()) })
			}
			shared, sharedErr := rootedfile.Open(root, name)
			if shared != nil {
				t.Cleanup(func() { require.NoError(t, shared.Close()) })
			}
			if sharedErr != nil {
				require.EqualError(t, delegatedErr, sharedErr.Error())
				return
			}
			require.NoError(t, delegatedErr)
			before, err := delegated.Stat()
			require.NoError(t, err)
			after, err := shared.Stat()
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			require.Equal(t, before.Mode(), after.Mode())
			require.Equal(t, before.Size(), after.Size())
			require.Equal(t, before.ModTime(), after.ModTime())
		})
	}
}
