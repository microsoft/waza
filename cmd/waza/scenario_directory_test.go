package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func scenarioSymlinkPermissionUnavailable(goos string, err error) bool {
	var linkErr *os.LinkError
	return goos == "windows" && errors.As(err, &linkErr) && linkErr.Op == "symlink" &&
		(errors.Is(linkErr.Err, fs.ErrPermission) || errors.Is(linkErr.Err, syscall.Errno(1314)))
}

func TestScenarioSymlinkPermissionUnavailable(t *testing.T) {
	permission := &os.LinkError{Op: "symlink", Err: fs.ErrPermission}
	for _, tt := range []struct {
		name, goos string
		err        error
		want       bool
	}{
		{"windows-permission", "windows", permission, true},
		{"windows-privilege", "windows", &os.LinkError{Op: "symlink", Err: syscall.Errno(1314)}, true},
		{"windows-wrapped", "windows", fmt.Errorf("creating link: %w", permission), true},
		{"windows-other-operation", "windows", &os.LinkError{Op: "link", Err: fs.ErrPermission}, false},
		{"windows-existing-entry", "windows", &os.LinkError{Op: "symlink", Err: fs.ErrExist}, false},
		{"windows-missing-path", "windows", &os.LinkError{Op: "symlink", Err: fs.ErrNotExist}, false},
		{"windows-unexpected-error", "windows", &os.LinkError{Op: "symlink", Err: errors.New("unexpected failure")}, false},
		{"windows-untyped-permission", "windows", fs.ErrPermission, false},
		{"windows-nil", "windows", nil, false},
		{"non-windows-permission", "darwin", permission, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, scenarioSymlinkPermissionUnavailable(tt.goos, tt.err))
		})
	}
}

func TestScenarioExclusiveWriteAfterPreflight(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			dir := t.TempDir()
			parent, err := openScenarioDirectory(dir, false)
			require.NoError(t, err)
			defer func() { require.NoError(t, parent.Close()) }()
			path := filepath.Join(dir, "eval.yaml")
			_, err = parent.Lstat("eval.yaml")
			require.ErrorIs(t, err, fs.ErrNotExist)
			target := filepath.Join(dir, "unrelated.yaml")
			original := []byte("keep competing file bytes\n")
			if symlink {
				scenarioDirectoryLink(t, target, path)
			} else {
				require.NoError(t, os.WriteFile(path, original, 0o600))
			}
			_, err = writeScenarioFileAt(parent, "eval.yaml", path, "new scaffold contents")
			require.ErrorContains(t, err, "refusing to overwrite")
			if symlink {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				require.Equal(t, target, link)
				require.NoFileExists(t, target)
			} else {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, original, after)
			}
		})
	}
}

func TestScenarioExclusiveWrite(t *testing.T) {
	dir := t.TempDir()
	parent, err := openScenarioDirectory(dir, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	path := filepath.Join(dir, "eval.yaml")
	info, err := writeScenarioFileAt(parent, "eval.yaml", path, "scaffold bytes\n")
	require.NoError(t, err)
	require.NotNil(t, info)
	require.NoError(t, info.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "scaffold bytes\n", string(data))
	missingParent := filepath.Join(dir, "missing", "eval.yaml")
	info, err = writeScenarioFileAt(parent, "missing/eval.yaml", missingParent, "scaffold bytes\n")
	require.Nil(t, info)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, "creating")
	require.NoFileExists(t, missingParent)
}

func scenarioDirectoryLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if scenarioSymlinkPermissionUnavailable(runtime.GOOS, err) {
			t.Skipf("symlink privileges unavailable: %v", err)
		}
		require.NoError(t, err)
	}
}

func TestScenarioScaffoldParentLinks(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		for _, parent := range []string{"evals", "evals/inventory", "evals/inventory/tasks", "evals/inventory/fixtures", "evals/inventory/cases/nested"} {
			for _, internal := range []bool{false, true} {
				t.Run(fmt.Sprintf("absolute=%t/%s/internal=%t", absolute, parent, internal), func(t *testing.T) {
					dir := t.TempDir()
					t.Chdir(dir)
					target := t.TempDir()
					if internal {
						target = filepath.Join(filepath.Dir(parent), "real")
						require.NoError(t, os.MkdirAll(target, 0o755))
					}
					require.NoError(t, os.MkdirAll(filepath.Dir(parent), 0o755))
					linkTarget := target
					if internal {
						linkTarget = "real"
					}
					scenarioDirectoryLink(t, linkTarget, parent)
					require.NoError(t, os.WriteFile(filepath.Join(target, "keep.txt"), []byte("original"), 0o600))
					if parent == "evals/inventory/cases/nested" {
						require.NoError(t, os.WriteFile(".waza.yaml", []byte("files:\n  taskGlob: cases/nested/*.yaml\n"), 0o600))
					}
					output := filepath.Join("evals", "inventory", "eval.yaml")
					if absolute {
						output = filepath.Join(dir, output)
					}
					root := newRootCommand()
					root.SetOut(&bytes.Buffer{})
					root.SetErr(&bytes.Buffer{})
					root.SetArgs([]string{"new", "eval", "inventory", "--scenario", "--output", output})
					require.ErrorContains(t, root.Execute(), "not a symlink")
					entries, err := os.ReadDir(target)
					require.NoError(t, err)
					require.Len(t, entries, 1)
					require.Equal(t, "keep.txt", entries[0].Name())
					require.NoFileExists(t, output)
				})
			}
		}
	}
}

func TestScenarioDirectoryMissingAndExisting(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := openScenarioDirectory(filepath.Join("missing", "nested"), false)
	require.NoError(t, err)
	require.Nil(t, root)
	require.NoDirExists(t, "missing")
	root, err = openScenarioDirectory(filepath.Join("missing", "nested"), true)
	require.NoError(t, err)
	require.NoError(t, root.Close())
	root, err = openScenarioDirectory(filepath.Join("missing", "nested"), false)
	require.NoError(t, err)
	require.NoError(t, root.Close())
	require.NoError(t, os.WriteFile("file", []byte("preserve"), 0o600))
	_, err = openScenarioDirectory("file/child", true)
	require.Error(t, err)
	data, err := os.ReadFile("file")
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
	scenarioDirectoryLink(t, "absent", "dangling")
	_, err = openScenarioDirectory("dangling/nested", true)
	require.ErrorContains(t, err, "not a symlink")
	require.NoDirExists(t, "absent")
}

func TestScenarioDirectoryChangedBeforeOpen(t *testing.T) {
	for _, kind := range []string{"external-link", "internal-link", "directory", "file", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "parent"), 0o755))
			anchor, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() { require.NoError(t, anchor.Close()) }()
			observed, err := anchor.Lstat("parent")
			require.NoError(t, err)
			require.NoError(t, os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "original")))
			switch kind {
			case "external-link":
				scenarioDirectoryLink(t, t.TempDir(), filepath.Join(dir, "parent"))
			case "internal-link":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "other"), 0o755))
				scenarioDirectoryLink(t, "other", filepath.Join(dir, "parent"))
			case "directory":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "parent"), 0o755))
			case "file":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "parent"), []byte("preserve"), 0o600))
			}
			retained, err := admitScenarioDirectory(anchor, "parent", observed)
			require.Error(t, err)
			require.Nil(t, retained)
			require.NoFileExists(t, filepath.Join(dir, "original", "report.txt"))
		})
	}
}

func TestScenarioDirectoryChangedAfterOpen(t *testing.T) {
	for _, kind := range []string{"external-link", "internal-link", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "parent"), 0o755))
			anchor, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() { require.NoError(t, anchor.Close()) }()
			observed, err := anchor.Lstat("parent")
			require.NoError(t, err)
			retained, err := anchor.OpenRoot("parent")
			require.NoError(t, err)
			defer func() { require.NoError(t, retained.Close()) }()
			require.NoError(t, os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "original")))
			switch kind {
			case "external-link":
				scenarioDirectoryLink(t, t.TempDir(), filepath.Join(dir, "parent"))
			case "internal-link":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "other"), 0o755))
				scenarioDirectoryLink(t, "other", filepath.Join(dir, "parent"))
			case "directory":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "parent"), 0o755))
			}
			require.Error(t, validateScenarioDirectory(anchor, "parent", observed, retained))
			entries, err := retained.ReadFile("report.txt")
			require.ErrorIs(t, err, fs.ErrNotExist)
			require.Nil(t, entries)
		})
	}
}

func TestScenarioRetainedDirectoryDoesNotFollowReplacement(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprintf("link=%t", link), func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "parent"), 0o755))
			retained, err := openScenarioDirectory(filepath.Join(dir, "parent"), false)
			require.NoError(t, err)
			defer func() { require.NoError(t, retained.Close()) }()
			require.NoError(t, os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "original")))
			target := filepath.Join(dir, "parent")
			if link {
				target = t.TempDir()
				scenarioDirectoryLink(t, target, filepath.Join(dir, "parent"))
			} else {
				require.NoError(t, os.Mkdir(target, 0o755))
			}
			file, err := writeScenarioFileAt(retained, "report.txt", "parent/report.txt", "retained")
			require.NoError(t, err)
			require.NoError(t, file.Close())
			require.NoFileExists(t, filepath.Join(target, "report.txt"))
			data, err := os.ReadFile(filepath.Join(dir, "original", "report.txt"))
			require.NoError(t, err)
			require.Equal(t, "retained", string(data))
		})
	}
}

func TestScenarioDirectoryAdmissionErrors(t *testing.T) {
	dir := t.TempDir()
	anchor, err := os.OpenRoot(dir)
	require.NoError(t, err)
	_, err = walkScenarioDirectory(anchor, "..", true)
	require.ErrorContains(t, err, "must be local")
	require.NoError(t, anchor.Close())
	_, err = walkScenarioDirectory(anchor, ".", true)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrClosed))
	dir = t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "child"), 0o755))
	anchor, err = os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, anchor.Close()) }()
	observed, err := anchor.Lstat("child")
	require.NoError(t, err)
	child, err := anchor.OpenRoot("child")
	require.NoError(t, err)
	require.NoError(t, child.Close())
	require.ErrorIs(t, validateScenarioDirectory(anchor, "child", observed, child), os.ErrClosed)
}

func TestScenarioWorkingDirectoryIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := os.OpenRoot(".")
	require.NoError(t, err)
	require.NoError(t, validateScenarioWorkingDirectory(root))
	require.NoError(t, root.Close())
	require.ErrorIs(t, validateScenarioWorkingDirectory(root), os.ErrClosed)
	root, err = os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.ErrorContains(t, validateScenarioWorkingDirectory(root), "changed during admission")
}

func TestScenarioDirectoryAdmissionCleanup(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	retained, err := finishScenarioDirectoryAdmission(root, fs.ErrPermission)
	require.ErrorIs(t, err, fs.ErrPermission)
	require.Nil(t, retained)
	_, err = root.Stat(".")
	require.ErrorIs(t, err, os.ErrClosed)
	root, err = os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	retained, err = finishScenarioDirectoryAdmission(root, nil)
	require.NoError(t, err)
	require.Same(t, root, retained)
	require.NoError(t, retained.Close())
	retained, err = finishScenarioDirectoryAdmission(nil, fs.ErrPermission)
	require.Nil(t, retained)
	require.ErrorIs(t, err, fs.ErrPermission)
}

func TestScenarioWindowsJunctionParent(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junction creation is Windows-only")
	}
	dir := t.TempDir()
	target := t.TempDir()
	parent := filepath.Join(dir, "parent")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", parent, target).CombinedOutput()
	require.NoError(t, err, "%s", out)
	_, err = openScenarioDirectory(filepath.Join(parent, "nested"), true)
	require.Error(t, err)
	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestScenarioTrustedWorkingDirectoryAlias(t *testing.T) {
	base := t.TempDir()
	target := t.TempDir()
	alias := filepath.Join(base, "working")
	scenarioDirectoryLink(t, target, alias)
	t.Chdir(alias)
	root, err := openScenarioDirectory("output/nested", true)
	require.NoError(t, err)
	file, err := writeScenarioFileAt(root, "report.txt", "output/nested/report.txt", "trusted cwd")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.NoError(t, root.Close())
	data, err := os.ReadFile(filepath.Join(target, "output", "nested", "report.txt"))
	require.NoError(t, err)
	require.Equal(t, "trusted cwd", string(data))
}

func TestScenarioDarwinSystemOutputAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("literal system alias compatibility is Darwin-only")
	}
	for _, alias := range []string{"/tmp", "/var/tmp"} {
		dir, err := os.MkdirTemp(alias, "waza-scenario-parent-")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
		root, err := openScenarioDirectory(filepath.Join(dir, "new", "nested"), true)
		require.NoError(t, err)
		file, err := writeScenarioFileAt(root, "report.txt", "report.txt", "system alias")
		require.NoError(t, err)
		require.NoError(t, file.Close())
		require.NoError(t, root.Close())
		data, err := os.ReadFile(filepath.Join(dir, "new", "nested", "report.txt"))
		require.NoError(t, err)
		require.Equal(t, "system alias", string(data))
	}
}

func TestScenarioWriteFailureRollsBackCreatedOutputs(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for failure := range 3 {
			t.Run(fmt.Sprintf("partial=%t/failure=%d", partial, failure), func(t *testing.T) {
				dir := t.TempDir()
				root, err := os.OpenRoot(dir)
				require.NoError(t, err)
				defer func() { require.NoError(t, root.Close()) }()
				paths := []string{"a.yaml", "b.yaml", "c.yaml"}
				parents := make(map[string]*os.Root)
				files := make(map[string]string)
				for _, path := range paths {
					parents[path], files[path] = root, "complete\n"
				}
				require.NoError(t, root.WriteFile("keep.txt", []byte("unrelated\n"), 0o600))
				calls := 0
				create := func(parent *os.Root, name, path, contents string) (*os.File, error) {
					index := calls
					calls++
					if index == failure {
						if !partial {
							return nil, io.ErrShortWrite
						}
						info, err := writeScenarioFileAt(parent, name, path, "partial\n")
						require.NoError(t, err)
						return info, io.ErrShortWrite
					}
					return writeScenarioFileAt(parent, name, path, contents)
				}
				require.ErrorIs(t, writeScenarioFiles(paths, parents, dir, files, create), io.ErrShortWrite)
				for _, path := range paths {
					require.NoFileExists(t, filepath.Join(dir, path))
				}
				data, err := root.ReadFile("keep.txt")
				require.NoError(t, err)
				require.Equal(t, "unrelated\n", string(data))
				require.NoError(t, writeScenarioFiles(paths, parents, dir, files, writeScenarioFileAt))
				for _, path := range paths {
					data, err := root.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, "complete\n", string(data))
				}
			})
		}
	}
}

func TestScenarioRollbackPreservesCompetingReplacement(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	paths := []string{"a.yaml", "b.yaml"}
	parents := map[string]*os.Root{"a.yaml": root, "b.yaml": root}
	files := map[string]string{"a.yaml": "owned\n", "b.yaml": "unused\n"}
	create := func(parent *os.Root, name, path, contents string) (*os.File, error) {
		if name == "b.yaml" {
			require.NoError(t, root.Rename("a.yaml", "original.yaml"))
			require.NoError(t, root.WriteFile("a.yaml", []byte("competing\n"), 0o600))
			return nil, io.ErrShortWrite
		}
		return writeScenarioFileAt(parent, name, path, contents)
	}
	err = writeScenarioFiles(paths, parents, dir, files, create)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.ErrorContains(t, err, "refusing scenario rollback of replaced file")
	for path, expected := range map[string]string{"a.yaml": "competing\n", "original.yaml": "owned\n"} {
		data, err := root.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}
}

func TestScenarioRollbackAfterExclusiveCollision(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.NoError(t, root.WriteFile("b.yaml", []byte("competing\n"), 0o600))
	err = writeScenarioFiles([]string{"a.yaml", "b.yaml"},
		map[string]*os.Root{"a.yaml": root, "b.yaml": root}, dir,
		map[string]string{"a.yaml": "owned\n", "b.yaml": "must not overwrite\n"}, writeScenarioFileAt)
	require.ErrorContains(t, err, "refusing to overwrite")
	require.NoFileExists(t, filepath.Join(dir, "a.yaml"))
	data, err := root.ReadFile("b.yaml")
	require.NoError(t, err)
	require.Equal(t, "competing\n", string(data))
}

func TestScenarioRollbackCleanupErrorsAndMissingOutput(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprintf("closed=%t", closed), func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() {
				if !closed {
					require.NoError(t, root.Close())
				}
			}()
			create := func(parent *os.Root, name, path, contents string) (*os.File, error) {
				if name == "b.yaml" {
					if closed {
						require.NoError(t, root.Close())
					} else {
						require.NoError(t, root.Remove("a.yaml"))
					}
					return nil, io.ErrShortWrite
				}
				return writeScenarioFileAt(parent, name, path, contents)
			}
			err = writeScenarioFiles([]string{"a.yaml", "b.yaml"},
				map[string]*os.Root{"a.yaml": root, "b.yaml": root}, dir,
				map[string]string{"a.yaml": "owned\n", "b.yaml": "unused\n"}, create)
			require.ErrorIs(t, err, io.ErrShortWrite)
			if closed {
				require.ErrorIs(t, err, os.ErrClosed)
				require.ErrorContains(t, err, "checking scenario rollback")
				data, readErr := os.ReadFile(filepath.Join(dir, "a.yaml"))
				require.NoError(t, readErr)
				require.Equal(t, "owned\n", string(data))
			} else {
				require.Equal(t, io.ErrShortWrite, err)
				require.NoFileExists(t, filepath.Join(dir, "a.yaml"))
			}
		})
	}
}

func TestScenarioRollbackUsesRetainedParent(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	require.NoError(t, os.Mkdir(parent, 0o755))
	root, err := os.OpenRoot(parent)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	paths := []string{"a.yaml", "b.yaml"}
	parents := map[string]*os.Root{"a.yaml": root, "b.yaml": root}
	files := map[string]string{"a.yaml": "owned\n", "b.yaml": "unused\n"}
	pinned := false
	create := func(retained *os.Root, name, path, contents string) (*os.File, error) {
		if name == "b.yaml" {
			renameErr := os.Rename(parent, parent+".original")
			pinned = runtime.GOOS == "windows" && renameErr != nil
			if pinned {
				require.ErrorIs(t, renameErr, syscall.Errno(32))
				return nil, io.ErrShortWrite
			}
			require.NoError(t, renameErr)
			require.NoError(t, os.Mkdir(parent, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(parent, "a.yaml"), []byte("competing\n"), 0o600))
			return nil, io.ErrShortWrite
		}
		return writeScenarioFileAt(retained, name, path, contents)
	}
	require.ErrorIs(t, writeScenarioFiles(paths, parents, parent, files, create), io.ErrShortWrite)
	if pinned {
		require.NoFileExists(t, filepath.Join(parent, "a.yaml"), "rollback removes the owned output through the pinned root")
		require.NoError(t, root.Close())
		require.NoError(t, os.Rename(parent, parent+".original"), "closing the root releases the Windows pin")
		require.NoError(t, os.Mkdir(parent, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(parent, "a.yaml"), []byte("competing\n"), 0o600))
		file, err := writeScenarioFileAt(root, "a.yaml", "a.yaml", "must not reopen replacement")
		require.Nil(t, file)
		require.ErrorIs(t, err, os.ErrClosed)
	}
	require.NoFileExists(t, filepath.Join(parent+".original", "a.yaml"))
	data, err := os.ReadFile(filepath.Join(parent, "a.yaml"))
	require.NoError(t, err)
	require.Equal(t, "competing\n", string(data))
}

func TestScenarioRollbackRetainsOwnershipAcrossUnlinkAndRecreate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()
			var held *os.File
			create := func(parent *os.Root, name, path, contents string) (*os.File, error) {
				if name == "b.yaml" && fail {
					original, err := held.Stat()
					require.NoError(t, err)
					require.NoError(t, root.Remove("a.yaml"))
					require.NoError(t, root.WriteFile("a.yaml", []byte("foreign\n"), 0o600))
					retained, err := held.Stat()
					require.NoError(t, err, "ownership must remain pinned after unlink")
					replacement, err := root.Stat("a.yaml")
					require.NoError(t, err)
					require.True(t, os.SameFile(original, retained))
					require.False(t, os.SameFile(retained, replacement), "the held inode must not be recycled")
					return nil, io.ErrShortWrite
				}

				file, err := writeScenarioFileAt(parent, name, path, contents)
				if name == "a.yaml" {
					held = file
				}
				return file, err
			}
			err = writeScenarioFiles([]string{"a.yaml", "b.yaml"},
				map[string]*os.Root{"a.yaml": root, "b.yaml": root}, dir,
				map[string]string{"a.yaml": "owned\n", "b.yaml": "complete\n"}, create)
			if fail {
				require.ErrorIs(t, err, io.ErrShortWrite)
				require.ErrorContains(t, err, "refusing scenario rollback of replaced file")
				data, err := root.ReadFile("a.yaml")
				require.NoError(t, err)
				require.Equal(t, "foreign\n", string(data))
			} else {
				require.NoError(t, err)
			}
			_, err = held.Stat()
			require.ErrorIs(t, err, os.ErrClosed, "success and rollback must both release ownership")
		})
	}
}

func TestScenarioRollbackReportsLostOwnershipHandle(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	var held *os.File
	create := func(parent *os.Root, name, path, contents string) (*os.File, error) {
		if name == "b.yaml" {
			require.NoError(t, held.Close())
			return nil, io.ErrShortWrite
		}
		file, err := writeScenarioFileAt(parent, name, path, contents)
		held = file
		return file, err
	}
	err = writeScenarioFiles([]string{"a.yaml", "b.yaml"},
		map[string]*os.Root{"a.yaml": root, "b.yaml": root}, dir,
		map[string]string{"a.yaml": "owned\n", "b.yaml": "unused\n"}, create)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.ErrorIs(t, err, os.ErrClosed)
	require.ErrorContains(t, err, "inspecting scenario rollback ownership")
	require.ErrorContains(t, err, "closing scenario output")
	data, err := root.ReadFile("a.yaml")
	require.NoError(t, err)
	require.Equal(t, "owned\n", string(data), "unverifiable ownership must leave an explicit residual")
}
