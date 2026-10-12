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
	for _, lineEnding := range []struct {
		name  string
		value string
	}{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
	} {
		t.Run(lineEnding.name, func(t *testing.T) {
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
			text := strings.ReplaceAll(string(data), "\r\n", "\n")
			text = strings.ReplaceAll(text, "\n", lineEnding.value)
			inlineGrader := strings.ReplaceAll("  - name: answer\n    type: text\n    config:\n      contains: [ready]", "\n", lineEnding.value)
			remoteGrader := "  - name: answer" + lineEnding.value + "    ref: " + refString
			require.Equal(t, 1, strings.Count(text, inlineGrader), "fixture must contain exactly one inline grader to replace")
			data = []byte(strings.Replace(text, inlineGrader, remoteGrader, 1))
			require.NoError(t, os.WriteFile(evalPath, data, 0o600))
			lock := models.NewLockfile()
			lock.UpsertGrader(entry)
			require.NoError(t, models.WriteLockfile(filepath.Join(root, models.LockfileName), lock))
			spec, err := models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			require.Len(t, spec.Graders, 1)
			require.Equal(t, refString, spec.Graders[0].Ref, "exercise remote lock expansion, not the original inline grader")
			require.NoError(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath))
			require.Equal(t, models.TextGraderParameters{Contains: []string{"supported"}}, spec.Graders[0].Parameters)
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "graders", "factuality.yaml"), []byte("type: text\nname: factuality\nconfig:\n  contains: [tampered]\n"), 0o600))
			spec, err = models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			require.ErrorContains(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath), "digest mismatch")
			lock.Graders[0].Commit = strings.Repeat("f", 40)
			require.NoError(t, models.WriteLockfile(filepath.Join(root, models.LockfileName), lock))
			require.ErrorContains(t, resolver.ExpandLockedGraders(t.Context(), spec, evalPath), "not available offline")
		})
	}
}
