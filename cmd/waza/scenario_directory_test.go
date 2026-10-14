package main

import (
	"bytes"
	"errors"
	"fmt"
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
			require.ErrorContains(t, writeScenarioFileAt(parent, "eval.yaml", path, "new scaffold contents"), "refusing to overwrite")
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
	require.NoError(t, writeScenarioFileAt(parent, "eval.yaml", path, "scaffold bytes\n"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "scaffold bytes\n", string(data))
	missingParent := filepath.Join(dir, "missing", "eval.yaml")
	err = writeScenarioFileAt(parent, "missing/eval.yaml", missingParent, "scaffold bytes\n")
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
			require.NoError(t, writeScenarioFileAt(retained, "report.txt", "parent/report.txt", "retained"))
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
	require.NoError(t, writeScenarioFileAt(root, "report.txt", "output/nested/report.txt", "trusted cwd"))
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
		require.NoError(t, writeScenarioFileAt(root, "report.txt", "report.txt", "system alias"))
		require.NoError(t, root.Close())
		data, err := os.ReadFile(filepath.Join(dir, "new", "nested", "report.txt"))
		require.NoError(t, err)
		require.Equal(t, "system alias", string(data))
	}
}
