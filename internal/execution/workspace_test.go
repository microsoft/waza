package execution

import (
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

	files := captureWorkspaceFiles(dir)
	require.Len(t, files, 2)
	assert.Equal(t, "root content", string(files["root.txt"]))
	assert.Equal(t, "nested content", string(files["sub/nested.txt"]))
}

func TestCaptureWorkspaceFiles_EmptyDir(t *testing.T) {
	files := captureWorkspaceFiles(t.TempDir())
	assert.Empty(t, files)
}

func TestCaptureWorkspaceFiles_EmptyString(t *testing.T) {
	files := captureWorkspaceFiles("")
	assert.Nil(t, files)
}

func TestCaptureWorkspaceFiles_UsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "c.txt"), []byte("deep"), 0o644))

	files := captureWorkspaceFiles(dir)
	_, ok := files["a/b/c.txt"]
	assert.True(t, ok, "keys should use forward slashes regardless of OS")
}
