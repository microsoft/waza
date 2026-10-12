//go:build linux || darwin

package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestWorkspaceExclusionIdentityAliases(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "Rubric/report.txt", "evaluator-only")
	require.NoError(t, os.Link(filepath.Join(root, "Rubric/report.txt"), filepath.Join(root, "alias.txt")))
	workspaceTestWrite(t, root, "public.txt", "public\n")
	require.NoError(t, os.Link(filepath.Join(root, "public.txt"), filepath.Join(root, "public-alias.txt")))
	for _, exclusions := range [][]string{{"Rubric/report.txt"}, {"Rubric"}} {
		files, err := CaptureWorkspace(root, []string{"alias.txt", "public-alias.txt"}, exclusions, nil, WorkspaceLimits{100, 200})
		require.ErrorContains(t, err, "evaluator-only")
		require.Len(t, files, 1)
		require.Equal(t, "public\n", files[0].Content)
	}
	files, err := CaptureWorkspace(root, []string{"public-alias.txt"}, []string{"missing"}, nil, WorkspaceLimits{100, 200})
	require.NoError(t, err)
	require.Len(t, files, 1)
	if _, err := os.Stat(filepath.Join(root, "rubric/report.txt")); err == nil {
		for _, exclusions := range [][]string{{"Rubric/report.txt"}, {"Rubric"}} {
			files, err := CaptureWorkspace(root, []string{"rubric/report.txt"}, exclusions, nil, WorkspaceLimits{100, 200})
			require.ErrorContains(t, err, "evaluator-only")
			require.Empty(t, files)
		}
	} else {
		workspaceTestWrite(t, root, "rubric/report.txt", "distinct public file")
		files, err := CaptureWorkspace(root, []string{"rubric/report.txt"}, []string{"Rubric"}, nil, WorkspaceLimits{100, 200})
		require.NoError(t, err)
		require.Len(t, files, 1, "case-sensitive distinct files must not be excluded by spelling alone")
	}
}

func TestWorkspaceExclusionInventoryEntryAndDepthBounds(t *testing.T) {
	for _, total := range []int{256, 257} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			root := workspaceTestRoot(t)
			require.NoError(t, os.Mkdir(filepath.Join(root, "rubric"), 0o700))
			for i := 1; i < total; i++ {
				workspaceTestWrite(t, root, fmt.Sprintf("rubric/f%03d.txt", i), "private")
			}
			workspaceTestWrite(t, root, "public.txt", "public")
			files, err := CaptureWorkspace(root, []string{"public.txt"}, []string{"rubric"}, nil, WorkspaceLimits{100, 200})
			if total == 256 {
				require.NoError(t, err)
				require.Len(t, files, 1)
			} else {
				require.ErrorContains(t, err, "256")
				require.Empty(t, files, "no selected bytes may be admitted after incomplete inventory")
			}
		})
	}
	for _, depth := range []int{32, 33} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			root := workspaceTestRoot(t)
			name := strings.Repeat("d/", depth-1) + "f.txt"
			workspaceTestWrite(t, root, name, "private")
			workspaceTestWrite(t, root, "public.txt", "public")
			files, err := CaptureWorkspace(root, []string{"public.txt"}, []string{"d"}, nil, WorkspaceLimits{100, 200})
			if depth == 32 {
				require.NoError(t, err)
				require.Len(t, files, 1)
			} else {
				require.ErrorContains(t, err, "depth 32")
				require.Empty(t, files)
			}
		})
	}
}

func TestWorkspaceExclusionInventorySpecialEntriesAndResources(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "rubric/a.txt", "private")
	require.NoError(t, os.Symlink("outside-missing", filepath.Join(root, "rubric/link")))
	require.NoError(t, unix.Mkfifo(filepath.Join(root, "rubric/pipe"), 0o600))
	workspaceTestWrite(t, root, "public.txt", "public")
	files, err := CaptureWorkspace(root, []string{"public.txt"}, []string{"rubric"}, nil, WorkspaceLimits{100, 200})
	require.NoError(t, err)
	require.Len(t, files, 1)

	for _, injected := range []error{unix.EMFILE, unix.EACCES} {
		t.Run(injected.Error(), func(t *testing.T) {
			dir, err := openWorkspaceRoot(root)
			require.NoError(t, err)
			calls := 0
			dir.inventoryOpen = func(fd int, name string, flags int, mode uint32) (int, error) {
				calls++
				if calls == 2 {
					return -1, injected
				}
				return unix.Openat(fd, name, flags, mode)
			}
			require.ErrorContains(t, dir.admitExclusions([]string{"rubric"}), "no selected content captured")
			held := append([]*os.File(nil), dir.held...)
			require.NotEmpty(t, held)
			require.NoError(t, dir.Close())
			_, rootErr := dir.file.Stat()
			require.ErrorIs(t, rootErr, os.ErrClosed)
			for _, file := range held {
				_, err := file.Stat()
				require.ErrorIs(t, err, os.ErrClosed)
			}
		})
	}
}

func TestWorkspaceExclusionInventoryResourceFailureClosesTraversalAncestors(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a/b/c.txt", "private")
	dir, err := openWorkspaceRoot(root)
	require.NoError(t, err)
	var admitted []int
	dir.inventoryOpen = func(fd int, name string, flags int, mode uint32) (int, error) {
		if name == "b" {
			return -1, unix.EMFILE
		}
		child, err := unix.Openat(fd, name, flags, mode)
		if err == nil {
			admitted = append(admitted, child)
		}
		return child, err
	}
	require.ErrorContains(t, dir.admitExclusions([]string{"a/b/c.txt"}), "ancestor inspection failed")
	require.Len(t, admitted, 1)
	var info unix.Stat_t
	require.ErrorIs(t, unix.Fstat(admitted[0], &info), unix.EBADF)
	require.NoError(t, dir.Close())
}

func TestWorkspaceExclusionInventoryInspectionFailureAndMissingBounds(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "rubric/child.txt", "private")
	for _, fault := range []error{unix.ENOENT, unix.EIO} {
		dir, err := openWorkspaceRoot(root)
		require.NoError(t, err)
		dir.inventoryStat = func(fd int, name string, metadata *unix.Stat_t, flags int) error {
			if name == "child.txt" {
				return fault
			}
			return unix.Fstatat(fd, name, metadata, flags)
		}
		require.ErrorContains(t, dir.admitExclusions([]string{"rubric"}), "inspection failed")
		require.NoError(t, dir.Close())
	}
	for _, count := range []int{256, 257} {
		dir, err := openWorkspaceRoot(root)
		require.NoError(t, err)
		paths := make([]string, count)
		for i := range paths {
			paths[i] = "missing/neighbor.txt"
		}
		err = dir.admitExclusions(paths)
		if count == 256 {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "256")
		}
		require.NoError(t, dir.Close())
	}
	dir, err := openWorkspaceRoot(root)
	require.NoError(t, err)
	require.NoError(t, dir.admitExclusions([]string{"rubric", "rubric/child.txt"}))
	require.NoError(t, dir.Close())
}

func TestWorkspaceRootAncestorsAndDescriptorRename(t *testing.T) {
	base := workspaceTestRoot(t)
	workspaceTestWrite(t, base, "real/root/public.txt", "original")
	require.NoError(t, os.Symlink("real", filepath.Join(base, "alias")))
	files, err := CaptureWorkspace(filepath.Join(base, "alias/root"), []string{"public.txt"}, nil, nil, WorkspaceLimits{100, 200})
	require.Error(t, err)
	require.Empty(t, files)
	t.Chdir(base)
	dir, err := openWorkspaceRoot("real/root")
	require.NoError(t, err)
	defer func() { require.NoError(t, dir.Close()) }()
	require.NoError(t, os.Rename("real/root", "real/old"))
	workspaceTestWrite(t, base, "real/root/public.txt", "replacement")
	require.NoError(t, dir.admitExclusions([]string{"missing"}))
	content, err := dir.readFile("public.txt", 100)
	require.NoError(t, err)
	require.Equal(t, "original", string(content), "inventory and candidate reads must retain the admitted root")
}

func TestWorkspaceRootExplicitSystemAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return // Only Darwin defines these explicit system aliases.
	}
	for _, alias := range []string{"/var", "/tmp"} {
		target, err := os.Readlink(alias)
		require.NoError(t, err)
		require.True(t, target == "/private"+alias || target == "private"+alias)
		dir, err := openWorkspaceRoot(alias)
		require.NoError(t, err)
		require.NoError(t, dir.Close())
	}
}

func TestWorkspaceSystemRootAliasRequiresExactMapping(t *testing.T) {
	for _, alias := range []string{"/var", "/tmp"} {
		for _, target := range []string{"/private" + alias, "private" + alias, "/untrusted", "/private" + alias + "/other"} {
			actual, err := workspaceSystemRootAlias(alias+"/workspace", func(name string) (string, error) {
				require.Equal(t, alias, name)
				return target, nil
			})
			if target == "/private"+alias || target == "private"+alias {
				require.NoError(t, err)
				require.Equal(t, "/private"+alias+"/workspace", actual)
			} else {
				require.Error(t, err)
				require.Empty(t, actual)
			}
		}
		_, err := workspaceSystemRootAlias(alias, func(string) (string, error) { return "", unix.EACCES })
		require.Error(t, err)
	}
	actual, err := workspaceSystemRootAlias("/tmp-untrusted/workspace", func(string) (string, error) {
		t.Fatal("non-system paths must not be canonicalized")
		return "", nil
	})
	require.NoError(t, err)
	require.Equal(t, "/tmp-untrusted/workspace", actual)
}

func TestWorkspaceReadRejectsSameSizeChangeWithRestoredModificationTime(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "public.txt", "before")
	dir, err := openWorkspaceRoot(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, dir.Close()) }()
	dir.afterRead = func(file *os.File) {
		info, err := file.Stat()
		require.NoError(t, err)
		filename := filepath.Join(root, "public.txt")
		require.NoError(t, os.WriteFile(filename, []byte("after!"), 0o600))
		require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))
	}
	_, err = dir.readFile("public.txt", 100)
	require.ErrorContains(t, err, "metadata changed")
	require.False(t, errors.Is(err, os.ErrNotExist))
}
