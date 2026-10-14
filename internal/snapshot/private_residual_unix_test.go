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
	"golang.org/x/sys/unix"
)

const residualContent = "complete evidence"

func residualUnlinkFailure(temporary, final bool) func(int, string, int) error {
	return func(fd int, name string, flags int) error {
		if strings.HasPrefix(name, ".") && temporary || !strings.HasPrefix(name, ".") && final {
			return unix.EIO
		}
		return unix.Unlinkat(fd, name, flags)
	}
}

func TestPrivateWriterCleanupResiduals(t *testing.T) {
	for _, test := range []struct {
		name                              string
		tempFail, finalFail, primaryError bool
		entries                           int
		temporaryPath                     bool
	}{
		{"success", false, false, false, 1, false},
		{"temporary only", true, false, false, 1, true},
		{"both failures", true, true, false, 2, true},
		{"final only", false, true, true, 1, false},
		{"primary error cleanup succeeds", false, false, true, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			var names []string
			hooks := privateWriterHooks{unlink: func(fd int, name string, flags int) error {
				names = append(names, name)
				if test.primaryError {
					require.NoError(t, os.Chmod(root, 0o700))
				}
				return residualUnlinkFailure(test.tempFail, test.finalFail)(fd, name, flags)
			}}
			if test.primaryError {
				hooks.afterPublication = func() { require.NoError(t, os.Chmod(root, 0o755)) }
			}
			path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), hooks)
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			require.Len(t, entries, test.entries)
			require.True(t, strings.HasPrefix(names[0], "."), "temporary cleanup precedes final cleanup")
			if test.tempFail || test.finalFail || test.primaryError {
				require.Error(t, err)
				require.Len(t, names, 2)
				require.NotContains(t, err.Error(), root)
				require.NotContains(t, err.Error(), residualContent)
			} else {
				require.NoError(t, err)
				require.Len(t, names, 1)
			}
			if test.entries == 0 {
				require.Empty(t, path)
				return
			}
			require.NotEmpty(t, path)
			require.Equal(t, test.temporaryPath, strings.HasSuffix(path, ".tmp"))
			content, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, residualContent, string(content))
			info, readErr := os.Stat(path)
			require.NoError(t, readErr)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			for _, entry := range entries {
				other, statErr := os.Stat(filepath.Join(root, entry.Name()))
				require.NoError(t, statErr)
				require.True(t, os.SameFile(info, other))
			}
			if test.tempFail {
				require.ErrorContains(t, err, "remove incomplete")
			}
			if test.finalFail {
				require.ErrorContains(t, err, "remove unpublished")
			}
			if test.tempFail && test.finalFail {
				require.ErrorContains(t, err, "additional private evidence residue")
			}
		})
	}
}

func TestPrivateWriterFailedWriteResiduals(t *testing.T) {
	for _, test := range []struct {
		name                            string
		complete, shortNil, closeFailed bool
	}{
		{"partial bytes", false, false, false},
		{"short write without error", false, true, false},
		{"complete bytes with write error", true, false, false},
		{"complete bytes with close error", true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			var owned *os.File
			path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), privateWriterHooks{
				unlink: residualUnlinkFailure(true, false),
				write: func(file *os.File, data []byte) (int, error) {
					owned = file
					if !test.complete {
						data = data[:len(data)/2]
					}
					written, err := file.Write(data)
					require.NoError(t, err)
					if test.closeFailed {
						require.NoError(t, file.Close())
						return written, nil
					}
					if test.shortNil {
						return written, nil
					}
					return written, unix.ENOSPC
				},
			})
			require.ErrorContains(t, err, "write private evidence failed")
			_, statErr := owned.Stat()
			require.ErrorIs(t, statErr, os.ErrClosed)
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			require.Len(t, entries, 1)
			content, readErr := os.ReadFile(filepath.Join(root, entries[0].Name()))
			require.NoError(t, readErr)
			if test.closeFailed {
				require.ErrorContains(t, err, "close private evidence file failed")
			}
			if test.complete {
				require.NotEmpty(t, path)
				require.Equal(t, residualContent, string(content))
			} else {
				require.Empty(t, path)
				require.Equal(t, residualContent[:len(residualContent)/2], string(content))
				require.ErrorContains(t, err, "location is unavailable")
			}
		})
	}
}

func TestPrivateWriterResidualSemanticChanges(t *testing.T) {
	for _, change := range []string{"same-size bytes", "mode", "symlink", "fifo", "directory", "missing", "name swap after stat", "root swap during read", "metadata during read"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			var temporary string
			ops := privateResidualOperations()
			stats := 0
			realStat := ops.stat
			if change == "name swap after stat" {
				ops.stat = func(file *os.File) (os.FileInfo, error) {
					info, err := realStat(file)
					stats++
					if stats == 3 {
						require.NoError(t, os.Rename(temporary, temporary+".old"))
						require.NoError(t, os.WriteFile(temporary, []byte(residualContent), 0o600))
					}
					return info, err
				}
			}
			if change == "root swap during read" || change == "metadata during read" {
				ops.read = func(file *os.File, limit int64) ([]byte, error) {
					content, err := io.ReadAll(io.LimitReader(file, limit))
					if change == "root swap during read" {
						require.NoError(t, os.Rename(root, root+".old"))
						require.NoError(t, os.Mkdir(root, 0o700))
					} else {
						require.NoError(t, os.Chmod(temporary, 0o400))
					}
					return content, err
				}
			}
			hooks := privateWriterHooks{residualOps: &ops, unlink: func(fd int, name string, flags int) error {
				if !strings.HasPrefix(name, ".") {
					return unix.Unlinkat(fd, name, flags)
				}
				temporary = filepath.Join(root, name)
				switch change {
				case "same-size bytes":
					before, err := os.Stat(temporary)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(temporary, []byte("modified evidence"), 0o600))
					require.NoError(t, os.Chtimes(temporary, before.ModTime(), before.ModTime()))
				case "mode":
					require.NoError(t, os.Chmod(temporary, 0o644))
				case "symlink", "fifo", "directory", "missing":
					require.NoError(t, unix.Unlinkat(fd, name, flags))
					switch change {
					case "symlink":
						require.NoError(t, os.Symlink("unrelated", temporary))
					case "fifo":
						require.NoError(t, unix.Mkfifo(temporary, 0o600))
					case "directory":
						require.NoError(t, os.Mkdir(temporary, 0o700))
					}
				}
				return unix.EIO
			}}
			path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), hooks)
			require.ErrorContains(t, err, "location is unavailable")
			require.Empty(t, path)
			require.NotContains(t, err.Error(), root)
			require.NotContains(t, err.Error(), residualContent)
			if change == "root swap during read" {
				entries, readErr := os.ReadDir(root)
				require.NoError(t, readErr)
				require.Empty(t, entries)
				entries, readErr = os.ReadDir(root + ".old")
				require.NoError(t, readErr)
				require.Len(t, entries, 1)
			}
		})
	}
}

func TestPrivateWriterResidualFallbackAndCollisionOwnership(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "final fallback", true: "unowned final collision"}[collision], func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			var final string
			hooks := privateWriterHooks{}
			if collision {
				hooks.beforePublication = func() {
					entries, err := os.ReadDir(root)
					require.NoError(t, err)
					require.Len(t, entries, 1)
					final = filepath.Join(root, strings.TrimSuffix(strings.TrimPrefix(entries[0].Name(), "."), ".tmp"))
					require.NoError(t, os.WriteFile(final, []byte("unrelated"), 0o600))
				}
			}
			hooks.unlink = func(fd int, name string, flags int) error {
				if strings.HasPrefix(name, ".") {
					if !collision {
						require.NoError(t, os.Rename(filepath.Join(root, name), filepath.Join(root, "replacement-backup")))
						require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("unrelated"), 0o600))
					}
					return unix.EIO
				}
				require.False(t, collision, "a failed link must not remove an unowned final collision")
				return unix.EIO
			}
			path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), hooks)
			require.Error(t, err)
			require.NotEmpty(t, path)
			require.Equal(t, collision, strings.HasSuffix(path, ".tmp"))
			content, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, residualContent, string(content))
			if collision {
				content, readErr = os.ReadFile(final)
				require.NoError(t, readErr)
				require.Equal(t, "unrelated", string(content))
				require.NotContains(t, err.Error(), "additional private evidence residue")
			} else {
				require.ErrorContains(t, err, "additional private evidence residue")
			}
		})
	}
}

func TestPrivateWriterResidualMovedRootCannotBeLocated(t *testing.T) {
	root := filepath.Join(workspaceTestRoot(t), "private")
	path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), privateWriterHooks{
		afterPublication: func() {
			require.NoError(t, os.Rename(root, root+".old"))
			require.NoError(t, os.Mkdir(root, 0o700))
		},
		unlink: residualUnlinkFailure(true, true),
	})
	require.Empty(t, path)
	require.ErrorContains(t, err, "location is unavailable")
	require.ErrorContains(t, err, "additional private evidence residue")
	require.NotContains(t, err.Error(), root)
	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	require.Empty(t, entries)
	entries, readErr = os.ReadDir(root + ".old")
	require.NoError(t, readErr)
	require.Len(t, entries, 2)
}

func TestPrivateWriterResidualObservationFailuresReleaseDescriptors(t *testing.T) {
	for _, failure := range []string{"owned stat", "open", "before stat", "read", "after stat", "name open", "name stat", "close", "name close"} {
		t.Run(failure, func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			ops := privateResidualOperations()
			realOpen, realStat, realRead, realClose := ops.openat, ops.stat, ops.read, ops.close
			var opened []*os.File
			var fds []int
			opens, stats, closes := 0, 0, 0
			ops.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
				opens++
				if failure == "open" && opens == 1 || failure == "name open" && opens == 2 {
					return -1, unix.EMFILE
				}
				result, err := realOpen(fd, name, flags, mode)
				if err == nil {
					fds = append(fds, result)
				}
				return result, err
			}
			ops.stat = func(file *os.File) (os.FileInfo, error) {
				opened = append(opened, file)
				stats++
				if failure == "owned stat" && stats == 1 || failure == "before stat" && stats == 2 ||
					failure == "after stat" && stats == 3 || failure == "name stat" && stats == 4 {
					return nil, unix.EIO
				}
				return realStat(file)
			}
			ops.read = func(file *os.File, limit int64) ([]byte, error) {
				if failure == "read" {
					return nil, unix.EIO
				}
				return realRead(file, limit)
			}
			ops.close = func(file *os.File) error {
				closes++
				require.NoError(t, realClose(file))
				if failure == "close" && closes == 1 || failure == "name close" && closes == 2 {
					return unix.EIO
				}
				return nil
			}
			path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent),
				privateWriterHooks{unlink: residualUnlinkFailure(true, false), residualOps: &ops})
			require.Empty(t, path)
			require.ErrorContains(t, err, "location is unavailable")
			require.NotContains(t, err.Error(), root)
			for _, file := range opened {
				_, statErr := file.Stat()
				require.ErrorIs(t, statErr, os.ErrClosed)
			}
			for _, fd := range fds {
				var stat unix.Stat_t
				require.ErrorIs(t, unix.Fstat(fd, &stat), unix.EBADF)
			}
			if failure == "owned stat" {
				require.ErrorContains(t, err, "original private evidence identity is unavailable")
			} else if strings.Contains(failure, "close") {
				require.ErrorContains(t, err, "close residual private evidence")
			} else {
				require.Positive(t, stats)
			}
		})
	}
}

func TestPrivateWriterResidualAndLateCloseRemainExplicitFailures(t *testing.T) {
	root := filepath.Join(workspaceTestRoot(t), "private")
	path, err := writePrivateSnapshotWithHooks(root, []byte(residualContent), privateWriterHooks{
		unlink: residualUnlinkFailure(true, false),
		closeDirectory: func(file *os.File) error {
			require.NoError(t, file.Close())
			return errors.New("injected close failure")
		},
	})
	require.NotEmpty(t, path)
	require.ErrorContains(t, err, "remove incomplete")
	require.ErrorContains(t, err, "close private evidence directory")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, residualContent, string(content))
}
