package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTestCase_GitResourcesAndWorkdir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: tc-repos
name: With git repo
inputs:
  prompt: "do something"
  workdir: my-repo
  repos:
    - type: worktree
      source: /tmp/some/source
      commit: main
      dest: my-repo
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	tc, err := LoadTestCase(path)
	require.NoError(t, err)
	require.Len(t, tc.Stimulus.Repos, 1)
	assert.Equal(t, GitResourceTypeWorktree, tc.Stimulus.Repos[0].Type)
	assert.Equal(t, "/tmp/some/source", tc.Stimulus.Repos[0].Source)
	assert.Equal(t, "main", tc.Stimulus.Repos[0].Commit)
	assert.Equal(t, "my-repo", tc.Stimulus.Repos[0].Dest)
	assert.Equal(t, "my-repo", tc.Stimulus.WorkDir)
}

func TestLoadTestCase_GitResources_RejectsMissingSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: tc
name: bad
inputs:
  prompt: "x"
  repos:
    - type: worktree
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	_, err := LoadTestCase(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
}

func TestLoadTestCase_GitResources_RejectsUnsupportedType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: tc
name: bad
inputs:
  prompt: "x"
  repos:
    - type: ftp
      source: /tmp
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	_, err := LoadTestCase(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestLoadTestCase_Workdir_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: tc
name: bad
inputs:
  prompt: "x"
  workdir: "../escape"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	_, err := LoadTestCase(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestGitResource_ValidateAcceptsWorktreeAndCleanDest(t *testing.T) {
	g := &GitResource{Type: GitResourceTypeWorktree, Source: "/tmp/r", Dest: "sub/dir"}
	assert.NoError(t, g.Validate())
}

func TestGitResource_ValidateRejectsAbsoluteDest(t *testing.T) {
	g := &GitResource{Type: GitResourceTypeWorktree, Source: "/tmp/r", Dest: "/abs"}
	assert.Error(t, g.Validate())
}

func TestGitResource_ValidateRejectsTraversalSegmentInDest(t *testing.T) {
	// "foo/../bar" cleans to "bar" but the raw input contains a '..'
	// segment, which we reject for clarity.
	g := &GitResource{Type: GitResourceTypeWorktree, Source: "/tmp/r", Dest: "foo/../bar"}
	err := g.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestGitResource_ValidateRequiresDestForWorktree(t *testing.T) {
	g := &GitResource{Type: GitResourceTypeWorktree, Source: "/tmp/r"}
	err := g.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dest")
}

func TestLoadTestCase_Workdir_RejectsTraversalSegment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: tc
name: bad
inputs:
  prompt: "x"
  workdir: "foo/../bar"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	_, err := LoadTestCase(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}
