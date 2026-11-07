package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupWorkspaceResources_WritesFiles(t *testing.T) {
	workspace := t.TempDir()

	resources := []ResourceFile{
		{Path: "root.txt", Content: []byte("root")},
		{Path: "nested/child.txt", Content: []byte("child")},
		{Path: "", Content: []byte("ignored")},
	}

	err := setupWorkspaceResources(workspace, resources)
	require.NoError(t, err)

	rootContent, err := os.ReadFile(filepath.Join(workspace, "root.txt"))
	require.NoError(t, err)
	assert.Equal(t, "root", string(rootContent))

	childContent, err := os.ReadFile(filepath.Join(workspace, "nested", "child.txt"))
	require.NoError(t, err)
	assert.Equal(t, "child", string(childContent))
}

func TestSetupWorkspaceResources_RejectsAbsolutePath(t *testing.T) {
	absPath := "/etc/passwd"
	if runtime.GOOS == "windows" {
		absPath = `C:\etc\passwd`
	}
	err := setupWorkspaceResources(t.TempDir(), []ResourceFile{{Path: absPath, Content: []byte("x")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be relative")
}

func TestSetupWorkspaceResources_RejectsPathTraversal(t *testing.T) {
	err := setupWorkspaceResources(t.TempDir(), []ResourceFile{{Path: "../outside.txt", Content: []byte("x")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace")
}

func TestSetupWorkspaceResources_EmptyWorkspace(t *testing.T) {
	err := setupWorkspaceResources("", []ResourceFile{{Path: "file.txt", Content: []byte("x")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace")
}

func TestCaptureWorkspaceFiles_CapturesAllFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root content"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested content"), 0o644))

	files, err := captureWorkspaceFiles(dir, nil)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "root content", string(files["root.txt"]))
	assert.Equal(t, "nested content", string(files["sub/nested.txt"]))
}

func TestCaptureWorkspaceFiles_EmptyDir(t *testing.T) {
	files, err := captureWorkspaceFiles(t.TempDir(), nil)
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestCaptureWorkspaceFiles_EmptyString(t *testing.T) {
	files, err := captureWorkspaceFiles("", nil)
	require.NoError(t, err)
	assert.Nil(t, files)
}

func TestCaptureWorkspaceFiles_UsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "c.txt"), []byte("deep"), 0o644))

	files, err := captureWorkspaceFiles(dir, nil)
	require.NoError(t, err)
	_, ok := files["a/b/c.txt"]
	assert.True(t, ok, "keys should use forward slashes regardless of OS")
}

func TestCaptureWorkspaceFiles_ExcludesMetadataAndDependencies(t *testing.T) {
	dir := t.TempDir()
	for path, content := range map[string]string{
		"src/main.go":                  "package main",
		".git/objects/large":           "repository metadata",
		"node_modules/pkg/index.js":    "dependency",
		"nested/node_modules/pkg/data": "nested dependency",
	} {
		name := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, os.WriteFile(name, []byte(content), 0o644))
	}

	files, err := captureWorkspaceFiles(dir, nil)
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{"src/main.go": []byte("package main")}, files)
}

func TestCaptureWorkspaceFiles_PreservesGraderTargetInsideExcludedDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "node_modules", "generated", "result.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("graded output"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(target), "dependency.js"), []byte("ignored"), 0o644))

	files, err := captureWorkspaceFiles(dir, []string{"node_modules/generated/result.txt"})
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{"node_modules/generated/result.txt": []byte("graded output")}, files)
}

func TestCaptureWorkspaceFiles_RejectsIncompleteCapture(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits workspaceCaptureLimits
		files  map[string]string
		want   string
	}{
		{
			name:   "file count",
			limits: workspaceCaptureLimits{maxFiles: 1, maxEntries: 10, maxFileBytes: 10, maxTotalBytes: 20},
			files:  map[string]string{"a.txt": "a", "b.txt": "b"},
			want:   "file count exceeds limit",
		},
		{
			name:   "per file",
			limits: workspaceCaptureLimits{maxFiles: 2, maxEntries: 10, maxFileBytes: 3, maxTotalBytes: 20},
			files:  map[string]string{"large.txt": "four"},
			want:   "exceeds per-file limit",
		},
		{
			name:   "total bytes",
			limits: workspaceCaptureLimits{maxFiles: 2, maxEntries: 10, maxFileBytes: 10, maxTotalBytes: 5},
			files:  map[string]string{"a.txt": "aaa", "b.txt": "bbb"},
			want:   "would exceed total limit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
			}

			files, err := captureWorkspaceFilesWithLimits(dir, nil, tc.limits)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, files, "partial captures must not reach graders")
		})
	}
}

func TestCaptureWorkspaceFiles_BoundsTraversalEntries(t *testing.T) {
	dir := t.TempDir()
	for i := range 4 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.txt", i)), nil, 0o644))
	}

	files, err := captureWorkspaceFilesWithLimits(dir, nil, workspaceCaptureLimits{
		maxFiles: 10, maxEntries: 3, maxFileBytes: 10, maxTotalBytes: 10,
	})
	require.ErrorContains(t, err, "traversal entries exceed limit")
	require.Nil(t, files)
}

func TestCaptureWorkspaceFiles_PreservedSymlinkCannotEscapeWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(dir, "output")))

	files, err := captureWorkspaceFiles(dir, []string{"output/outside.txt"})
	require.Error(t, err)
	require.Nil(t, files)
}

func TestCaptureWorkspaceFiles_PreservesConfinedSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("ready"), 0o644))
	require.NoError(t, os.Symlink("target.txt", filepath.Join(dir, "result.txt")))

	files, err := captureWorkspaceFiles(dir, []string{"result.txt"})
	require.NoError(t, err)
	require.Equal(t, []byte("ready"), files["result.txt"])
}

func BenchmarkCaptureWorkspaceFilesDependencyTree(b *testing.B) {
	dir := b.TempDir()
	require.NoError(b, os.WriteFile(filepath.Join(dir, "source.go"), []byte("package sample\n"), 0o644))
	dependencies := filepath.Join(dir, "node_modules", "package")
	require.NoError(b, os.MkdirAll(dependencies, 0o755))
	content := make([]byte, 4<<10)
	for i := range 4096 {
		require.NoError(b, os.WriteFile(filepath.Join(dependencies, fmt.Sprintf("file-%04d.js", i)), content, 0o644))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := captureWorkspaceFiles(dir, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
