package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func evidenceJudgeLocalFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".evidence-judge-fixture-")
	require.NoError(t, err)
	dir, err = filepath.Abs(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func TestEvidenceJudgeDirectoriesRejectProjectAncestryAndRelativePaths(t *testing.T) {
	fixture := evidenceJudgeLocalFixture(t)
	_, err := createEvidenceJudgeDirs("relative-cache")
	require.ErrorContains(t, err, "absolute")
	_, err = createEvidenceJudgeDirs(filepath.Join(fixture, "cache"))
	require.ErrorContains(t, err, "project or skill configuration")
	_, err = os.Lstat(filepath.Join(fixture, "cache"))
	require.ErrorIs(t, err, os.ErrNotExist, "rejected ancestry must not create a cache")
}

func TestEvidenceJudgeDirectoriesIgnoreAmbientTMPDIRAndPreserveSymlinkTargets(t *testing.T) {
	fixture := evidenceJudgeLocalFixture(t)
	t.Setenv("TMPDIR", fixture)
	dirs, err := newEvidenceJudgeDirs()
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(dirs.path, fixture+string(filepath.Separator)))
	info, err := os.Stat(dirs.path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
	target := filepath.Join(fixture, "actor-secret")
	require.NoError(t, os.WriteFile(target, []byte("untouched"), 0o600))
	if runtime.GOOS != "windows" {
		require.NoError(t, dirs.root.Symlink(target, "outside"))
	}
	require.NoError(t, dirs.root.WriteFile("home/owned", []byte("private"), 0o600))
	require.NoError(t, dirs.close())
	value, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(value))
	_, err = os.Lstat(dirs.path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEvidenceJudgeDirectoriesPreserveCompetingLeafAndParent(t *testing.T) {
	for _, parentReplacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaf", true: "parent"}[parentReplacement], func(t *testing.T) {
			outer, err := newEvidenceJudgeDirs()
			require.NoError(t, err)
			dirs := outer
			replacedPath := outer.path
			if parentReplacement {
				dirs, err = createEvidenceJudgeDirs(filepath.Join(outer.path, "cache"))
				require.NoError(t, err)
			}
			moved := replacedPath + "-moved"
			err = os.Rename(replacedPath, moved)
			if err != nil {
				// A retained descendant handle can prevent ancestor replacement outright.
				require.True(t, parentReplacement, "leaf replacement must remain exercisable")
				require.ErrorIs(t, err, os.ErrPermission)
				original, statErr := os.Lstat(replacedPath)
				require.NoError(t, statErr)
				require.True(t, os.SameFile(outer.identity, original))
				require.NoError(t, dirs.root.WriteFile("home/owned", []byte("private"), 0o600))
				require.NoError(t, dirs.close())
				require.NoError(t, outer.close())
				_, statErr = os.Lstat(replacedPath)
				require.ErrorIs(t, statErr, os.ErrNotExist)
				_, statErr = os.Lstat(moved)
				require.ErrorIs(t, statErr, os.ErrNotExist)
				return
			}
			t.Cleanup(func() {
				require.NoError(t, os.RemoveAll(moved))
				require.NoError(t, os.RemoveAll(replacedPath))
			})
			require.NoError(t, os.Mkdir(replacedPath, 0o700))
			competitor := filepath.Join(replacedPath, "competing")
			require.NoError(t, os.WriteFile(competitor, []byte("preserve"), 0o600))
			err = dirs.close()
			require.ErrorContains(t, err, "identity changed")
			if parentReplacement {
				require.ErrorContains(t, outer.close(), "identity changed")
			}
			body, err := os.ReadFile(competitor)
			require.NoError(t, err)
			require.Equal(t, "preserve", string(body))
		})
	}
}

func TestEvidenceJudgeCacheRejectsUnsafePermissionsAndConfiguration(t *testing.T) {
	for _, marker := range []string{"skills", ".waza.yaml", ".git"} {
		t.Run(marker, func(t *testing.T) {
			outer, err := newEvidenceJudgeDirs()
			require.NoError(t, err)
			defer func() { require.NoError(t, outer.close()) }()
			require.NoError(t, outer.root.WriteFile(marker, nil, 0o600))
			_, err = createEvidenceJudgeDirs(filepath.Join(outer.path, "cache"))
			require.ErrorContains(t, err, "project or skill configuration")
		})
	}
	if runtime.GOOS != "windows" {
		outer, err := newEvidenceJudgeDirs()
		require.NoError(t, err)
		require.NoError(t, os.Chmod(outer.path, 0o777))
		_, err = createEvidenceJudgeDirs(filepath.Join(outer.path, "cache"))
		require.ErrorContains(t, err, "writable")
		require.NoError(t, os.Chmod(outer.path, 0o700))
		require.NoError(t, outer.close())
	}
}

func TestEvidenceJudgeAuthAndInitializationFailuresAreOffline(t *testing.T) {
	clearCustomProviderEnv(t)
	for _, variable := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(variable, "")
	}
	executor := NewEvidenceJudgeExecutor()
	executor.newDirs = func() (*evidenceJudgeDirs, error) {
		t.Fatal("missing auth should not create directories")
		return nil, nil
	}
	resp, err := executor.Execute(t.Context(), evidenceJudgeTestRequest())
	require.ErrorContains(t, err, "explicit")
	require.False(t, resp.Success)
	t.Setenv("COPILOT_BASE_URL", "not-a-provider-url")
	resp, err = executor.Execute(t.Context(), evidenceJudgeTestRequest())
	require.ErrorContains(t, err, "provider_failed")
	require.False(t, resp.Success)

	evidenceJudgeTestProvider(t)
	executor = NewEvidenceJudgeExecutor()
	executor.newDirs = func() (*evidenceJudgeDirs, error) { return nil, errors.New("cache unavailable") }
	resp, err = executor.Execute(t.Context(), evidenceJudgeTestRequest())
	require.ErrorContains(t, err, "cache unavailable")
	require.Nil(t, resp.Usage)
	executor = NewEvidenceJudgeExecutor()
	var path string
	executor.newDirs = func() (*evidenceJudgeDirs, error) {
		dirs, err := newEvidenceJudgeDirs()
		if dirs != nil {
			path = dirs.path
		}
		return dirs, err
	}
	executor.resolveCLI = func(context.Context, *evidenceJudgeDirs) (string, error) {
		return "", errors.New("unsupported runtime")
	}
	resp, err = executor.Execute(t.Context(), evidenceJudgeTestRequest())
	require.ErrorContains(t, err, "unsupported runtime")
	require.False(t, resp.Success)
	_, err = os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEvidenceJudgeExplicitRuntimeOverridesFailClosedWithoutLaunch(t *testing.T) {
	fixture := evidenceJudgeLocalFixture(t)
	for _, path := range []string{"copilot", filepath.Join(fixture, "copilot.js"), fixture, filepath.Join(fixture, "missing")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Setenv("COPILOT_CLI_PATH", path)
			_, err := evidenceJudgeCLIPath(t.Context(), nil)
			require.Error(t, err)
		})
	}
}
