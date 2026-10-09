package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityRemoteLockTampering(t *testing.T) {
	repo := createModuleRepo(t)
	resolver, err := NewResolver(WithCacheRoot(t.TempDir()), WithGitURLFunc(func(Ref) string { return repo }))
	require.NoError(t, err)
	refString := "example.com/acme/graders#factuality@v1.0.0"
	entry, err := resolver.ResolveRef(t.Context(), refString)
	require.NoError(t, err)
	ref, err := ParseRef(refString)
	require.NoError(t, err)
	cacheDir, err := resolver.cacheDir(ref, entry.Commit)
	require.NoError(t, err)
	root := t.TempDir()
	evalPath := filepath.Join(root, "eval.yaml")
	data, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "eval-1.0.yaml"))
	require.NoError(t, err)
	data = []byte(strings.Replace(string(data), "  - name: answer\n    type: text\n    config:\n      contains: [ready]", "  - name: answer\n    ref: "+refString, 1))
	require.NoError(t, os.WriteFile(evalPath, data, 0o600))
	lock := models.NewLockfile()
	lock.UpsertGrader(entry)
	require.NoError(t, models.WriteLockfile(filepath.Join(root, models.LockfileName), lock))
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	require.NoError(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath))
	require.Equal(t, models.TextGraderParameters{Contains: []string{"supported"}}, spec.Graders[0].Parameters)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "graders", "factuality.yaml"), []byte("type: text\nname: factuality\nconfig:\n  contains: [tampered]\n"), 0o600))
	spec, err = models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	require.ErrorContains(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath), "digest mismatch")
	lock.Graders[0].Commit = strings.Repeat("f", 40)
	require.NoError(t, models.WriteLockfile(filepath.Join(root, models.LockfileName), lock))
	require.ErrorContains(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath), "not available offline")
}
