//go:build linux || darwin

package snapshot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerArtifactPublicationAndPartialCleanup(t *testing.T) {
	for _, mode := range []string{"success", "short-write", "write-error", "close-directory"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private")
			hooks := privateWriterHooks{suffix: ".runner.json"}
			switch mode {
			case "short-write":
				hooks.write = func(f *os.File, data []byte) (int, error) { return f.Write(data[:1]) }
			case "write-error":
				hooks.write = func(*os.File, []byte) (int, error) { return 0, io.ErrClosedPipe }
			case "close-directory":
				hooks.closeDirectory = func(f *os.File) error { return errors.Join(f.Close(), io.ErrClosedPipe) }
			}
			name, err := writePrivateSnapshotWithHooks(root, []byte(`{"complete":true}`), hooks)
			if mode == "success" {
				require.NoError(t, err)
				require.True(t, strings.HasSuffix(name, ".runner.json"))
				content, err := os.ReadFile(name)
				require.NoError(t, err)
				require.JSONEq(t, `{"complete":true}`, string(content))
			} else {
				require.Error(t, err)
			}

			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			if mode == "short-write" || mode == "write-error" {
				require.Empty(t, entries, "partial artifacts must not be visible to Lab")
			} else {
				require.Len(t, entries, 1)
			}
			for _, entry := range entries {
				require.True(t, strings.HasSuffix(entry.Name(), ".runner.json"))
			}
		})
	}
	_, err := WritePrivateArtifact(t.TempDir(), nil, "/../../escape")
	require.Error(t, err)
}
