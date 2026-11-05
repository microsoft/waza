package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunDev_SingleTargetForms(t *testing.T) {
	targets := []struct {
		name       string
		arg        func(string) string
		inSkillDir bool
	}{
		{"skill name", func(string) string { return "target-skill" }, false},
		{"bare directory", func(string) string { return "local-folder" }, false},
		{"relative directory", func(string) string { return "." + string(filepath.Separator) + "local-folder" }, false},
		{"trailing directory separator", func(string) string { return "local-folder" + string(filepath.Separator) }, false},
		{"absolute directory", func(dir string) string { return dir }, false},
		{"relative file", func(string) string { return filepath.Join("local-folder", "SKILL.md") }, false},
		{"absolute file", func(dir string) string { return filepath.Join(dir, "SKILL.md") }, false},
		{"current directory", func(string) string { return "." }, true},
		{"bare file", func(string) string { return "SKILL.md" }, true},
		{"explicit current file", func(string) string { return "." + string(filepath.Separator) + "SKILL.md" }, true},
	}
	modes := []struct {
		name  string
		flags []string
	}{
		{"iterative", []string{"--target", "low"}},
		{"copilot", []string{"--copilot"}},
		{"scaffold triggers", []string{"--scaffold-triggers"}},
	}

	for _, mode := range modes {
		for _, target := range targets {
			t.Run(mode.name+"/"+target.name, func(t *testing.T) {
				t.Setenv("NO_COLOR", "1")
				skillDir := writeDevTargetFixture(t, filepath.Join(t.TempDir(), "local-folder"), "target-skill")
				t.Chdir(filepath.Dir(skillDir))
				if target.inSkillDir {
					t.Chdir(skillDir)
				}
				skillPath := filepath.Join(skillDir, "SKILL.md")
				before, err := os.ReadFile(skillPath)
				require.NoError(t, err)

				engine := &copilotTestEngine{suggestionOutput: "Recommendations for target-skill."}
				withDevTestEngine(t, engine)
				out := new(bytes.Buffer)
				cmd := NewCommand()
				cmd.SetOut(out)
				cmd.SetErr(out)
				cmd.SetIn(bytes.NewReader(nil))
				cmd.SetArgs(append([]string{target.arg(skillDir)}, mode.flags...))
				require.NoError(t, cmd.Execute())

				switch mode.name {
				case "iterative":
					require.Contains(t, out.String(), "Skill: target-skill")
					require.Contains(t, out.String(), "Target adherence level")
				case "copilot":
					require.True(t, engine.initCalled)
					require.Contains(t, engine.LastSuggestionMessage(), "name: target-skill")
					require.Contains(t, out.String(), "Recommendations for target-skill.")
				case "scaffold triggers":
					data, err := os.ReadFile(filepath.Join(skillDir, "tests", "trigger_tests.yaml"))
					require.NoError(t, err)
					require.Contains(t, string(data), "skill: target-skill")
					require.Contains(t, string(data), `"example tasks"`)
				}

				after, err := os.ReadFile(skillPath)
				require.NoError(t, err)
				require.Equal(t, before, after, "target resolution must not change SKILL.md")
			})
		}
	}
}

func TestRunDev_TargetNamePrecedence(t *testing.T) {
	root := t.TempDir()
	writeDevTargetFixture(t, filepath.Join(root, "skills", "workspace-skill"), "local-folder")
	writeDevTargetFixture(t, filepath.Join(root, "local-folder"), "directory-skill")
	t.Chdir(root)
	t.Setenv("NO_COLOR", "1")

	for _, tt := range []struct {
		arg      string
		wantName string
	}{
		{"local-folder", "local-folder"},
		{"." + string(filepath.Separator) + "local-folder", "directory-skill"},
	} {
		t.Run(tt.arg, func(t *testing.T) {
			out := new(bytes.Buffer)
			cmd := NewCommand()
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetIn(bytes.NewReader(nil))
			cmd.SetArgs([]string{tt.arg, "--target", "low"})
			require.NoError(t, cmd.Execute())
			require.Contains(t, out.String(), "Skill: "+tt.wantName+"\n")
		})
	}
}

func TestRunDev_InvalidTargets(t *testing.T) {
	for _, flags := range [][]string{
		{"--target", "low"},
		{"--copilot"},
		{"--scaffold-triggers"},
	} {
		t.Run(flags[0], func(t *testing.T) {
			root := t.TempDir()
			writeDevTargetFixture(t, filepath.Join(root, "skill"), "target-skill")
			require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("not a skill"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte("not a skill"), 0o644))
			t.Chdir(root)
			engine := &copilotTestEngine{}
			withDevTestEngine(t, engine)

			for _, tt := range []struct {
				arg      string
				wantErr  string
				notExist bool
			}{
				{"", "skill name or path required", false},
				{"missing-skill", `skill "missing-skill" not found in workspace`, false},
				{filepath.Join("missing", "SKILL.md"), "inspecting skill target", true},
				{filepath.Join("\x00", "SKILL.md"), "skill target", false},
				{"README.md", "must be a directory or SKILL.md file", false},
				{"README", "must be a directory or SKILL.md file", false},
			} {
				t.Run(tt.arg, func(t *testing.T) {
					cmd := NewCommand()
					cmd.SetOut(new(bytes.Buffer))
					cmd.SetErr(new(bytes.Buffer))
					cmd.SetArgs(append([]string{tt.arg}, flags...))
					err := cmd.Execute()
					require.ErrorContains(t, err, tt.wantErr)
					if tt.notExist {
						require.True(t, errors.Is(err, os.ErrNotExist), "filesystem error must be preserved: %v", err)
					}
					require.False(t, engine.initCalled, "invalid targets must fail before launching an agent")
				})
			}
		})
	}
}

func TestRunDev_ConfiguredTargetName(t *testing.T) {
	root := t.TempDir()
	writeDevTargetFixture(t, filepath.Join(root, "authored", "category", "example"), "configured-skill")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".waza.yaml"), []byte("paths:\n  skills: authored\n"), 0o644))
	t.Chdir(root)
	t.Setenv("NO_COLOR", "1")

	out := new(bytes.Buffer)
	cmd := NewCommand()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetArgs([]string{"configured-skill", "--target", "low"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "Skill: configured-skill\n")
}

func TestRunDev_DirectoryNamedSkillMD(t *testing.T) {
	skillDir := writeDevTargetFixture(t, filepath.Join(t.TempDir(), "SKILL.md"), "directory-skill")
	t.Chdir(filepath.Dir(skillDir))
	t.Setenv("NO_COLOR", "1")

	out := new(bytes.Buffer)
	cmd := NewCommand()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetArgs([]string{"SKILL.md", "--target", "low"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "Skill: directory-skill\n")
}

func TestRunDev_TargetPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without root privileges")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	writeDevTargetFixture(t, filepath.Join(locked, "skill"), "locked-skill")
	t.Chdir(root)
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(locked, 0o755))
	})

	cmd := NewCommand()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{filepath.Join("locked", "skill", "SKILL.md")})
	err := cmd.Execute()
	require.ErrorContains(t, err, "inspecting skill target")
	require.ErrorIs(t, err, os.ErrPermission)
}

func writeDevTargetFixture(t *testing.T, dir, name string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := fmt.Sprintf("---\nname: %s\ndescription: 'USE FOR: example tasks. DO NOT USE FOR: unrelated tasks.'\n---\n# Example\n", name)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	return dir
}
