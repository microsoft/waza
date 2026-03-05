package tokens

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func configureSafeCRLF(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "config", "core.safecrlf", "false")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
}

func TestDiff_ThresholdAndSkillRoots(t *testing.T) {
	dir := initRepo(t)
	configureSafeCRLF(t, dir)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "alpha"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "skills", "beta"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "alpha", "SKILL.md"), []byte("# Alpha\nbase content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "skills", "beta", "SKILL.md"), []byte("# Beta\nunchanged"), 0o644))
	commit(t, dir, "initial skills")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "alpha", "SKILL.md"), []byte("# Alpha\nbase content with extra words to increase tokens"), 0o644))

	out := new(bytes.Buffer)
	cmd := newDiffCmd()
	cmd.SetOut(out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"main", "--threshold", "5"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "threshold exceeded")
	require.Contains(t, out.String(), "skills/alpha")
	require.Contains(t, out.String(), ".github/skills/beta")
	require.Contains(t, out.String(), "⚠️")
}

func TestDiff_JSONAndConfigLimits(t *testing.T) {
	dir := initRepo(t)
	configureSafeCRLF(t, dir)

	cfg := `paths:
  skills: custom-skills
tokens:
  limits:
    defaults:
      "custom-skills/**/SKILL.md": 5
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom-skills", "delta"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "custom-skills", "delta", "SKILL.md"), []byte("# Delta\none two"), 0o644))
	commit(t, dir, "initial custom skill")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "custom-skills", "delta", "SKILL.md"), []byte("# Delta\none two three four five six seven"), 0o644))

	out := new(bytes.Buffer)
	cmd := newDiffCmd()
	cmd.SetOut(out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"main", "--format", "json", "--threshold", "500"})

	require.NoError(t, cmd.Execute())

	var report diffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "main", report.BaseRef)
	require.Equal(t, "WORKING", report.HeadRef)
	require.True(t, report.Passed)
	require.Len(t, report.Skills, 1)
	require.Equal(t, "custom-skills/delta", report.Skills[0].Path)
	require.Equal(t, 5, report.Skills[0].Limit)
	require.True(t, report.Skills[0].OverLimit)
}

func TestDiff_DefaultBaseRefFallbackToMain(t *testing.T) {
	dir := initRepo(t)
	configureSafeCRLF(t, dir)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "gamma"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "gamma", "SKILL.md"), []byte("# Gamma\nv1"), 0o644))
	commit(t, dir, "initial")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "gamma", "SKILL.md"), []byte("# Gamma\nv2 expanded"), 0o644))

	out := new(bytes.Buffer)
	cmd := newDiffCmd()
	cmd.SetOut(out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"--threshold", "500"})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "main → WORKING")
}

func TestDiff_UnknownBaseRefFailsFast(t *testing.T) {
	dir := initRepo(t)
	configureSafeCRLF(t, dir)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "alpha"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "alpha", "SKILL.md"), []byte("# Alpha"), 0o644))
	commit(t, dir, "initial")

	cmd := newDiffCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"not-a-real-ref"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown base ref "not-a-real-ref"`)
}
