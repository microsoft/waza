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
