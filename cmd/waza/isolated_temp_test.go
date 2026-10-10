package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	os.Exit(runIsolatedCLITests(m))
}

func runIsolatedCLITests(m *testing.M) (code int) {
	dir, err := allocateIsolatedCLITempDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, fmt.Errorf("removing owned CLI test storage: %w", err))
			code = 1
		}
	}()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, dir); err != nil {
			fmt.Fprintln(os.Stderr, fmt.Errorf("isolating CLI test environment %s: %w", key, err))
			return 1
		}
	}
	return m.Run()
}

// Project discovery intentionally walks ancestors; TMPDIR may be inside a project.
func isolatedCLITempDir(t *testing.T) string {
	t.Helper()
	dir, err := allocateIsolatedCLITempDir()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(dir), "removing owned CLI fixture")
	})
	return dir
}

func allocateIsolatedCLITempDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating external CLI fixture storage: %w", err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("creating CLI fixture storage: %w", err)
	}
	dir, err := os.MkdirTemp(base, "waza-cli-test-")
	if err != nil {
		return "", fmt.Errorf("allocating isolated CLI fixture: %w", err)
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.Join(fmt.Errorf("resolving CLI fixture ancestry: %w", err), os.RemoveAll(dir))
	}
	if err := validateCLIIsolation(physical); err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return physical, nil
}

func validateCLIIsolation(dir string) error {
	for {
		for _, marker := range []string{"skills", ".waza.yaml", ".git"} {
			path := filepath.Join(dir, marker)
			_, err := os.Lstat(path)
			if err == nil {
				return fmt.Errorf("CLI fixture inherits project marker %q; configure external cache storage", path)
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("checking CLI fixture marker %q: %w", path, err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func TestCLIIsolationRejectsInheritedProjectMarkers(t *testing.T) {
	for _, marker := range []string{"skills", ".waza.yaml", ".git"} {
		t.Run(marker, func(t *testing.T) {
			parent := isolatedCLITempDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(parent, marker), nil, 0o600))
			child := filepath.Join(parent, "nested")
			require.NoError(t, os.Mkdir(child, 0o700))
			require.ErrorContains(t, validateCLIIsolation(child), marker)
		})
	}
}

func TestCLIIsolationEscapesProjectLocalTMPDIR(t *testing.T) {
	parent := isolatedCLITempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(parent, "skills"), 0o700))
	config := []byte("defaults:\n  engine: mock\n  model: inherited-model\n")
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".waza.yaml"), config, 0o600))
	nested := filepath.Join(parent, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	t.Setenv("TMPDIR", nested)
	t.Setenv("TMP", nested)
	t.Setenv("TEMP", nested)

	dir := isolatedCLITempDir(t)
	t.Chdir(dir)
	root, found := findProjectRoot()
	require.False(t, found)
	require.Empty(t, root)
	cmd := newNewSkillCommand()
	cmd.SetArgs([]string{"isolated-skill"})
	require.NoError(t, cmd.Execute())
	require.FileExists(t, filepath.Join(dir, "isolated-skill", "SKILL.md"))
	require.FileExists(t, filepath.Join(dir, "isolated-skill", "evals", "eval.yaml"))
	generated, err := os.ReadFile(filepath.Join(dir, "isolated-skill", "evals", "eval.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(generated), "inherited-model")
	require.NoDirExists(t, filepath.Join(parent, "evals"))
	entries, err := os.ReadDir(filepath.Join(parent, "skills"))
	require.NoError(t, err)
	require.Empty(t, entries)
	unchanged, err := os.ReadFile(filepath.Join(parent, ".waza.yaml"))
	require.NoError(t, err)
	require.Equal(t, config, unchanged)
}

func TestFindProjectRootPreservesAncestorDiscovery(t *testing.T) {
	parent := isolatedCLITempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(parent, "skills"), 0o700))
	child := filepath.Join(parent, "nested", "child")
	require.NoError(t, os.MkdirAll(child, 0o700))
	t.Chdir(child)
	root, found := findProjectRoot()
	require.True(t, found)
	require.Equal(t, parent, root)
}
