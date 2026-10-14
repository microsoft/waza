//go:build linux || darwin

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestPrivateWriterAncestorSymlink(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing suffix", true: "existing suffix"}[existing], func(t *testing.T) {
			base := workspaceTestRoot(t)
			target, err := filepath.Abs(filepath.Join(base, "target"))
			require.NoError(t, err)
			require.NoError(t, os.Mkdir(target, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(target, "sentinel"), []byte("unchanged"), 0o600))
			if existing {
				require.NoError(t, os.MkdirAll(filepath.Join(target, "nested", "private"), 0o700))
			}
			link := filepath.Join(base, "link")
			require.NoError(t, os.Symlink(target, link))
			path, err := writePrivateSnapshot(filepath.Join(link, "nested", "private"), []byte("sensitive"))
			require.Error(t, err)
			require.Empty(t, path)
			data, err := os.ReadFile(filepath.Join(target, "sentinel"))
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(data))
			if existing {
				requireDirectoryEmpty(t, filepath.Join(target, "nested", "private"))
			} else {
				_, err := os.Stat(filepath.Join(target, "nested"))
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestPrivateWriterAncestorReplacement(t *testing.T) {
	for _, phase := range []string{"admission", "before link", "after link"} {
		t.Run(phase, func(t *testing.T) {
			base := workspaceTestRoot(t)
			parent := filepath.Join(base, "parent")
			root := filepath.Join(parent, "private")
			replace := func() {
				require.NoError(t, os.Rename(parent, parent+".old"))
				absolute, err := filepath.Abs(parent + ".old")
				require.NoError(t, err)
				require.NoError(t, os.Symlink(absolute, parent))
			}
			hooks := privateWriterHooks{}
			switch phase {
			case "admission":
				hooks.beforeAdmission = replace
			case "before link":
				hooks.beforePublication = replace
			case "after link":
				hooks.afterPublication = replace
			}
			path, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), hooks)
			require.ErrorContains(t, err, "identity changed")
			require.Empty(t, path)
			requireDirectoryEmpty(t, filepath.Join(parent+".old", "private"))
		})
	}
}

func TestPrivateWriterNonCreatingRechecks(t *testing.T) {
	for _, phase := range []string{"admission", "before link", "after link"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(workspaceTestRoot(t), "private")
			remove := func() { require.NoError(t, os.Rename(root, root+".old")) }
			hooks := privateWriterHooks{}
			switch phase {
			case "admission":
				hooks.beforeAdmission = remove
			case "before link":
				hooks.beforePublication = remove
			case "after link":
				hooks.afterPublication = remove
			}
			path, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), hooks)
			require.ErrorContains(t, err, "identity changed")
			require.Empty(t, path)
			_, err = os.Stat(root)
			require.ErrorIs(t, err, os.ErrNotExist)
			requireDirectoryEmpty(t, root+".old")
		})
	}
	root, err := filepath.Abs(filepath.Join(workspaceTestRoot(t), "missing", "private"))
	require.NoError(t, err)
	ops := privateDirectoryOperations()
	ops.mkdirat = func(int, string, uint32) error {
		t.Fatal("non-creating admission attempted mkdir")
		return nil
	}
	dir, err := openPrivateDirectoryWithOps(root, false, ops)
	require.Error(t, err)
	require.Nil(t, dir)
}

func TestPrivateWriterPhysicalAndCleanedRoots(t *testing.T) {
	base := workspaceTestRoot(t)
	absolute, err := filepath.Abs(base)
	require.NoError(t, err)
	for _, root := range []string{
		filepath.Join(base, "relative", "nested"),
		filepath.Join(absolute, "absolute", "nested"),
		base + "/unused/../cleaned/./nested",
	} {
		path, err := writePrivateSnapshot(root, []byte("safe"))
		require.NoError(t, err)
		require.Equal(t, filepath.Clean(root), filepath.Dir(path), "preserve existing lexical returned-path format")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "safe", string(data))
	}
	_, err = os.Stat(filepath.Join(base, "unused"))
	require.ErrorIs(t, err, os.ErrNotExist, "preserve lexical Clean semantics")
}

func TestPrivateWriterUnsupportedComponents(t *testing.T) {
	for _, kind := range []string{"file", "fifo"} {
		for _, suffix := range []string{"", "/private"} {
			t.Run(kind+suffix, func(t *testing.T) {
				component := filepath.Join(workspaceTestRoot(t), "component")
				if kind == "file" {
					require.NoError(t, os.WriteFile(component, []byte("unchanged"), 0o600))
				} else {
					require.NoError(t, unix.Mkfifo(component, 0o600))
				}
				path, err := writePrivateSnapshot(component+suffix, []byte("sensitive"))
				require.Error(t, err)
				require.Empty(t, path)
				if kind == "file" {
					data, err := os.ReadFile(component)
					require.NoError(t, err)
					require.Equal(t, "unchanged", string(data))
				}
			})
		}
	}
}

func TestPrivateDirectoryCreationRaces(t *testing.T) {
	for _, kind := range []string{"directory", "permissive", "symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			base := workspaceTestRoot(t)
			root, err := filepath.Abs(filepath.Join(base, "private"))
			require.NoError(t, err)
			target, err := filepath.Abs(base)
			require.NoError(t, err)
			ops := privateDirectoryOperations()
			mkdirs := 0
			ops.mkdirat = func(parent int, name string, mode uint32) error {
				mkdirs++
				require.Equal(t, "private", name)
				require.Equal(t, uint32(0o700), mode)
				switch kind {
				case "directory", "permissive":
					if kind == "permissive" {
						mode = 0o755
					}
					require.NoError(t, unix.Mkdirat(parent, name, mode))
				case "symlink":
					require.NoError(t, unix.Symlinkat(target, parent, name))
				case "file":
					fd, err := unix.Openat(parent, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0o600)
					require.NoError(t, err)
					require.NoError(t, unix.Close(fd))
				}
				return unix.EEXIST
			}
			dir, err := openPrivateDirectoryWithOps(root, true, ops)
			require.Equal(t, 1, mkdirs)
			if kind == "directory" {
				require.NoError(t, err)
				require.NoError(t, dir.Close())
			} else {
				require.Error(t, err)
				require.Nil(t, dir)
				if kind == "permissive" {
					info, err := os.Stat(root)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "do not chmod a competing directory")
				}
			}
		})
	}
}

func TestPrivateDirectoryFailuresReleaseDescriptors(t *testing.T) {
	for _, failure := range []string{"anchor", "EMFILE", "permission", "mkdir", "stat", "parent close", "parent and child close", "failed admission close"} {
		t.Run(failure, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join(workspaceTestRoot(t), "private"))
			require.NoError(t, err)
			ops := privateDirectoryOperations()
			live := map[int]bool{}
			admissionFailed := false
			record := func(fd int, err error) (int, error) {
				if err == nil {
					live[fd] = true
				}
				return fd, err
			}
			ops.open = func(path string, flags int, mode uint32) (int, error) {
				if failure == "anchor" {
					return -1, unix.EMFILE
				}
				return record(unix.Open(path, flags, mode))
			}
			ops.openat = func(parent int, name string, flags int, mode uint32) (int, error) {
				if name == "private" {
					switch failure {
					case "EMFILE":
						return -1, unix.EMFILE
					case "permission":
						return -1, unix.EACCES
					case "failed admission close":
						admissionFailed = true
						return -1, unix.ELOOP
					}
				}
				return record(unix.Openat(parent, name, flags, mode))
			}
			if failure == "mkdir" {
				ops.mkdirat = func(int, string, uint32) error { return unix.EACCES }
			}
			if failure == "stat" {
				ops.stat = func(*os.File) (os.FileInfo, error) { return nil, unix.EIO }
			}
			closed := 0
			ops.close = func(file *os.File) error {
				delete(live, int(file.Fd()))
				require.NoError(t, file.Close())
				closed++
				if failure == "parent and child close" ||
					failure == "parent close" && closed == 1 ||
					failure == "failed admission close" && admissionFailed {
					return errors.New("injected close after actual release")
				}
				return nil
			}
			dir, err := openPrivateDirectoryWithOps(root, true, ops)
			require.Error(t, err)
			require.Nil(t, dir)
			require.Empty(t, live, "every actual opened descriptor must be released")
			if failure != "anchor" {
				require.Positive(t, closed)
			}
			if failure == "parent and child close" {
				require.ErrorContains(t, err, "close admitted private evidence directory")
			}
			if _, err := os.Stat(root); err == nil {
				requireDirectoryEmpty(t, root)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestMetadataOnlyWriterAncestorBehaviorUnchanged(t *testing.T) {
	base := workspaceTestRoot(t)
	target, err := filepath.Abs(filepath.Join(base, "target"))
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(target, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	path, err := NewWriter(link).Write(&Snapshot{})
	require.NoError(t, err, "ordinary metadata-only snapshots retain historical writer behavior")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	require.Equal(t, link, filepath.Dir(path))
}

func TestPrivatePublicationPolicyPreservesMaterializationCheck(t *testing.T) {
	base := workspaceTestRoot(t)
	target, err := filepath.Abs(filepath.Join(base, "target"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(target, "private"), 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	dir, err := os.Open(filepath.Join(target, "private"))
	require.NoError(t, err)
	defer func() { require.NoError(t, dir.Close()) }()
	require.NoError(t, checkPrivateDirectoryIdentity(filepath.Join(link, "private"), dir), "materialization retains existing leaf-only identity semantics")
	absolute, err := filepath.Abs(filepath.Join(link, "private"))
	require.NoError(t, err)
	require.Error(t, checkPrivatePublicationDirectoryIdentity(absolute, dir), "only private publication adopts the new component admission policy")
}

func TestPrivatePublicationPolicyPreservesActualMaterialization(t *testing.T) {
	base, err := filepath.Abs(workspaceTestRoot(t))
	require.NoError(t, err)
	target := filepath.Join(base, "target")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "cwd"), 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	cwd := filepath.Join(link, "cwd")
	t.Chdir(cwd)
	t.Setenv("PWD", cwd)
	observed, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, cwd, observed, "exercise an actual non-leaf cwd alias, not a canonicalized substitute")

	snap := &Snapshot{WorkspaceFiles: []WorkspaceFile{{Path: "output.txt", Content: "preserved"}}}
	workspace, err := materializeVerifiedWorkspace(snap, []string{"output.txt"})
	require.NoError(t, err, "materialization retains its existing cwd/leaf admission policy")
	path := workspace.Path()
	data, err := os.ReadFile(filepath.Join(path, "output.txt"))
	require.NoError(t, err)
	require.Equal(t, "preserved", string(data))
	require.NoError(t, workspace.Close(), "actual cleanup must retain the existing leaf identity policy")
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	published, err := writePrivateSnapshot(filepath.Join(cwd, "snapshots"), []byte("sensitive"))
	require.Error(t, err, "private publication independently rejects the same ancestor alias")
	require.Empty(t, published)
	_, err = os.Stat(filepath.Join(target, "cwd", "snapshots"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func requireDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	require.Empty(t, entries)
}
