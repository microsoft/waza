package assurance

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalibrationCacheRetainsOwnedDirectoryAndClosesIdempotently(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	require.NoError(t, store.verify())
	directory := store.directory
	name, err := store.write([]byte("owned audit"))
	require.NoError(t, err)
	content, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, "owned audit", string(content))
	require.NoError(t, store.close())
	require.NoError(t, store.close())
	_, err = os.Stat(directory)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = store.write([]byte("closed"))
	require.ErrorContains(t, err, "cache is closed")
}

func TestCalibrationCacheRefusesInvalidParents(t *testing.T) {
	for _, mode := range []string{"file", "symlink", "group-writable", "world-writable"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			switch mode {
			case "file":
				require.NoError(t, os.WriteFile(".cache", []byte("unchanged"), 0o600))
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("Windows symlink creation requires a separate host capability")
				}
				target := t.TempDir()
				require.NoError(t, os.Symlink(target, ".cache"))
			default:
				if runtime.GOOS == "windows" {
					t.Skip("POSIX mode bits do not certify Windows ACLs")
				}
				require.NoError(t, os.Mkdir(".cache", 0o700))
				permissions := os.FileMode(0o770)
				if mode == "world-writable" {
					permissions = 0o777
				}
				require.NoError(t, os.Chmod(".cache", permissions))
			}
			store, err := newCalibrationRubricStore()
			require.Error(t, err)
			require.Nil(t, store)
			if mode == "file" {
				content, err := os.ReadFile(".cache")
				require.NoError(t, err)
				require.Equal(t, "unchanged", string(content))
			} else {
				entries, err := os.ReadDir(".cache")
				require.NoError(t, err)
				require.Empty(t, entries, "reject before creating an audit namespace")
			}
		})
	}
}

func TestCalibrationCacheParentReplacementCannotRedirectWritesOrCleanup(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	name, err := store.write([]byte("original"))
	require.NoError(t, err)
	leaf := filepath.Base(store.directory)
	require.NoError(t, os.Rename(".cache", "retained-cache"))
	require.NoError(t, os.MkdirAll(filepath.Join(".cache", leaf), 0o700))
	foreign := []byte("replacement must remain untouched")
	require.NoError(t, os.WriteFile(name, foreign, 0o600))
	_, err = store.write([]byte("must not be written"))
	require.ErrorContains(t, err, "identity changed")
	require.ErrorContains(t, store.close(), "identity changed")
	content, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, foreign, content)
	entries, err := os.ReadDir(filepath.Join(".cache", leaf))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = os.Stat(filepath.Join("retained-cache", leaf))
	require.ErrorIs(t, err, os.ErrNotExist, "cleanup remains confined to the acquired parent")
}

func TestCalibrationCacheChildReplacementPreservesForeignNamespace(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	name, err := store.write([]byte("original"))
	require.NoError(t, err)
	moved := store.directory + "-retained"
	require.NoError(t, os.Rename(store.directory, moved))
	require.NoError(t, os.Mkdir(store.directory, 0o700))
	foreign := []byte("replacement must remain untouched")
	require.NoError(t, os.WriteFile(name, foreign, 0o600))
	_, err = store.write([]byte("must not be written"))
	require.ErrorContains(t, err, "identity changed")
	err = store.close()
	require.ErrorContains(t, err, "identity changed")
	require.ErrorContains(t, err, "refusing cleanup")
	content, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, foreign, content)
	entries, err := os.ReadDir(moved)
	require.NoError(t, err)
	require.Empty(t, entries, "only original acquired files are removed")
}

func TestCalibrationCachePermissionChangesAndResidualsAreExplicit(t *testing.T) {
	t.Run("permission-change", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode bits do not certify Windows ACLs")
		}
		t.Chdir(t.TempDir())
		store, err := newCalibrationRubricStore()
		require.NoError(t, err)
		require.NoError(t, os.Chmod(store.directory, 0o770))
		_, err = store.write([]byte("refused"))
		require.ErrorContains(t, err, "permissions expose")
		require.ErrorContains(t, store.close(), "permissions expose")
	})
	t.Run("unknown-residual", func(t *testing.T) {
		t.Chdir(t.TempDir())
		store, err := newCalibrationRubricStore()
		require.NoError(t, err)
		directory := store.directory
		require.NoError(t, os.WriteFile(filepath.Join(directory, "unknown"), []byte("retain"), 0o600))
		require.Error(t, store.close(), "do not delete unknown residuals")
		content, err := os.ReadFile(filepath.Join(directory, "unknown"))
		require.NoError(t, err)
		require.Equal(t, "retain", string(content))
	})
}
