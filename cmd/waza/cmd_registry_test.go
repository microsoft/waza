package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	testRegistryCommit = "0123456789abcdef0123456789abcdef01234567"
	testRegistryDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakeRegistryAddResolver struct {
	programRefs map[string]bool
	err         error
}

func (f *fakeRegistryAddResolver) ResolveRefWithOptions(
	_ context.Context,
	ref string,
	opts registry.ResolveOptions,
) (models.LockfileGrader, error) {
	if f.err != nil {
		return models.LockfileGrader{}, f.err
	}
	if f.programRefs[ref] && !opts.AllowProgram {
		return models.LockfileGrader{}, &registry.ProgramGraderTrustError{Path: ref}
	}
	return models.LockfileGrader{
		Ref:     ref,
		Commit:  testRegistryCommit,
		Digest:  testRegistryDigest,
		URL:     "https://" + strings.Split(ref, "@")[0],
		Trusted: f.programRefs[ref] && opts.AllowProgram,
	}, nil
}

func useFakeRegistryAddResolver(t *testing.T, fake *fakeRegistryAddResolver) {
	t.Helper()
	original := newRegistryAddResolver
	newRegistryAddResolver = func() (registryAddResolver, error) {
		return fake, nil
	}
	t.Cleanup(func() { newRegistryAddResolver = original })
}

func TestRegistryCommandWiring(t *testing.T) {
	cmd := newRootCommand()

	registry, _, err := cmd.Find([]string{"registry"})
	require.NoError(t, err)
	require.Equal(t, "registry", registry.Name())

	search, _, err := cmd.Find([]string{"registry", "search"})
	require.NoError(t, err)
	require.Equal(t, "search", search.Name())

	add, _, err := cmd.Find([]string{"registry", "add"})
	require.NoError(t, err)
	require.Equal(t, "add", add.Name())
}

func TestRegistrySearchTableOutput(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir)

	var out bytes.Buffer
	cmd := newRegistrySearchCommand()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"factual", "--kind", "grader"})

	require.NoError(t, cmd.Execute())

	output := out.String()
	require.Contains(t, output, "REF")
	require.Contains(t, output, "KIND")
	require.Contains(t, output, "REGISTRY")
	require.Contains(t, output, "DESCRIPTION")
	require.Contains(t, output, "STARS")
	require.Contains(t, output, "github.com/waza-evals/fact#factuality@v1.0.0")
	require.Contains(t, output, "grader")
	require.Contains(t, output, "public")
	require.Contains(t, output, "Prompt grader for factual grounding.")
	require.Contains(t, output, "128")
}

func TestRegistrySearchFlagValidation(t *testing.T) {
	cmd := newRegistrySearchCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"fact", "--kind", "plugin"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --kind")
}

func TestRegistryAddAppendsRefGraderAndWritesLock(t *testing.T) {
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(`name: registry-test
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
graders:
  - name: local
    type: text
`), 0o644))

	var out bytes.Buffer
	cmd := newRegistryAddCommand()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
		"--name", "factuality_strict",
		"--set", "config.threshold=0.9",
		"--set", "weight=2",
	})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "Added github.com/waza-evals/fact#factuality@v1.0.0")

	data, err := os.ReadFile(evalPath)
	require.NoError(t, err)
	var evalDoc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &evalDoc))
	graders, ok := evalDoc["graders"].([]any)
	require.True(t, ok)
	require.Len(t, graders, 2)
	added, ok := graders[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "github.com/waza-evals/fact#factuality@v1.0.0", added["ref"])
	require.Equal(t, "factuality_strict", added["name"])
	require.Equal(t, 2, added["weight"])
	config, ok := added["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 0.9, config["threshold"])

	lockData, err := os.ReadFile(filepath.Join(dir, "waza.lock"))
	require.NoError(t, err)
	lockText := string(lockData)
	require.Contains(t, lockText, "schema_version: 1")
	require.Contains(t, lockText, "ref: github.com/waza-evals/fact#factuality@v1.0.0")
	require.Contains(t, lockText, "commit: "+testRegistryCommit)
	require.Contains(t, lockText, "digest: "+testRegistryDigest)
	require.NotContains(t, lockText, "modules:")
}

func TestRegistryAddRejectsEmptySetPathSegments(t *testing.T) {
	for _, setValue := range []string{"config..threshold=0.9", "config. threshold=0.9"} {
		t.Run(setValue, func(t *testing.T) {
			_, err := buildRegistryGraderEntry(
				"github.com/waza-evals/fact#factuality@v1.0.0",
				"",
				[]string{setValue},
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), "path segments must not be empty")
		})
	}
}

func TestRegistryAddReplacesExistingLockEntry(t *testing.T) {
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte("name: registry-test\n"), 0o644))

	for range 2 {
		cmd := newRegistryAddCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{
			"github.com/waza-evals/fact#factuality@v1.0.0",
			"--eval", evalPath,
		})
		require.NoError(t, cmd.Execute())
	}

	lock, err := models.LoadLockfile(filepath.Join(dir, "waza.lock"))
	require.NoError(t, err)
	require.Len(t, lock.Graders, 1)
	require.Equal(t, "github.com/waza-evals/fact#factuality@v1.0.0", lock.Graders[0].Ref)
}

func TestRegistryAddRestoresEvalWhenLockWriteFails(t *testing.T) {
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	original := []byte("name: registry-test\n")
	require.NoError(t, os.WriteFile(evalPath, original, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "waza.lock"), 0o755))

	cmd := newRegistryAddCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
	})

	require.Error(t, cmd.Execute())
	actual, err := os.ReadFile(evalPath)
	require.NoError(t, err)
	require.Equal(t, original, actual)
}

func TestRegistryAddPromptsForProgramGrader(t *testing.T) {
	ref := "github.com/waza-evals/program-graders#exec@v1.0.0"
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{
		programRefs: map[string]bool{ref: true},
	})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte("name: registry-test\n"), 0o644))

	cmd := newRegistryAddCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{
		ref,
		"--eval", evalPath,
	})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "remote program grader was not added")
}

func TestRegistryAddInteractiveProgramGraderMarksLockTrusted(t *testing.T) {
	ref := "github.com/waza-evals/program-graders#exec@v1.0.0"
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{
		programRefs: map[string]bool{ref: true},
	})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte("name: registry-test\n"), 0o644))

	cmd := newRegistryAddCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetArgs([]string{
		ref,
		"--eval", evalPath,
	})

	require.NoError(t, cmd.Execute())

	lockData, err := os.ReadFile(filepath.Join(dir, "waza.lock"))
	require.NoError(t, err)
	require.Contains(t, string(lockData), "trusted: true")
}

func TestRegistryAddPreservesEvalAndLockFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode preservation is platform-specific")
	}
	useFakeRegistryAddResolver(t, &fakeRegistryAddResolver{})

	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	lockPath := filepath.Join(dir, "waza.lock")
	require.NoError(t, os.WriteFile(evalPath, []byte("name: registry-test\n"), 0o600))
	lock := models.NewLockfile()
	lock.UpsertGrader(models.LockfileGrader{
		Ref:    "github.com/waza-evals/old#grader@v1.0.0",
		Commit: testRegistryCommit,
		Digest: testRegistryDigest,
		URL:    "https://github.com/waza-evals/old#grader",
	})
	require.NoError(t, models.WriteLockfile(lockPath, lock))
	require.NoError(t, os.Chmod(lockPath, 0o600))

	cmd := newRegistryAddCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
	})

	require.NoError(t, cmd.Execute())
	evalInfo, err := os.Stat(evalPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), evalInfo.Mode().Perm())
	lockInfo, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), lockInfo.Mode().Perm())
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })
}
