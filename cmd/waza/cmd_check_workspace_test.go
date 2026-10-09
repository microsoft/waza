package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCommand_WorkspaceSingleSkill(t *testing.T) {
	dir := t.TempDir()
	skillContent := `---
name: ws-single-skill
description: A test skill for workspace-aware check command.
---

# Workspace Single Skill

Body content.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0o644))
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil) // no args — workspace detection

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "ws-single-skill")
	assert.Contains(t, result, "Compliance Score:")
}

func TestCheckCommand_ExplicitPathEvalIndependentOfCWD(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			t.Run(fmt.Sprintf("configured=%t/format=%s", configured, format), func(t *testing.T) {
				parent := t.TempDir()
				plugin := filepath.Join(parent, "plugins", "sample")
				skillDir := filepath.Join(plugin, "skills", "category", "my-skill")
				require.NoError(t, os.MkdirAll(skillDir, 0o755))
				skillPath := filepath.Join(skillDir, "SKILL.md")
				require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: my-skill\ndescription: Testing target-relative eval discovery.\n---\n# Body\n"), 0o644))
				evalsDir, evalFile := "evals", "eval.yaml"
				if configured {
					evalsDir, evalFile = "tests", "suite.yml"
					require.NoError(t, os.WriteFile(filepath.Join(plugin, ".waza.yaml"), []byte("paths:\n  evals: tests\nfiles:\n  evalFile: suite.yml\n"), 0o644))
				}
				evalPath := filepath.Join(plugin, evalsDir, "my-skill", evalFile)
				require.NoError(t, os.MkdirAll(filepath.Dir(evalPath), 0o755))
				require.NoError(t, os.WriteFile(evalPath, []byte("name: sample\nskill: my-skill\n"), 0o644))

				unrelated := t.TempDir()
				otherSkill := filepath.Join(unrelated, "skills", "my-skill")
				require.NoError(t, os.MkdirAll(otherSkill, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Unrelated skill.\n---\n# Other\n"), 0o644))
				otherEval := filepath.Join(unrelated, "evals", "my-skill", "eval.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(otherEval), 0o755))
				require.NoError(t, os.WriteFile(otherEval, []byte("name: unrelated\n"), 0o644))

				for _, cwd := range []string{plugin, parent, skillDir, unrelated} {
					t.Chdir(cwd)
					relative, err := filepath.Rel(cwd, skillPath)
					require.NoError(t, err)
					for _, target := range []string{skillPath, skillDir, relative, filepath.Dir(relative)} {
						cmd := newCheckCommand()
						var output bytes.Buffer
						cmd.SetOut(&output)
						cmd.SetErr(&output)
						cmd.SetArgs([]string{target, "--format", format})
						require.NoError(t, cmd.Execute(), "cwd=%s target=%s", cwd, target)
						if format == "json" {
							var report checkJSONReport
							require.NoError(t, json.Unmarshal(output.Bytes(), &report))
							require.Len(t, report.Skills, 1)
							require.True(t, report.Skills[0].Eval.Found)
							require.Equal(t, evalPath, report.Skills[0].Eval.Path)
						} else {
							require.Contains(t, output.String(), "Evaluation Suite: Found")
						}
					}
				}
			})
		}
	}
}

func TestCheckCommand_ExplicitPathIgnoresUnrelatedEval(t *testing.T) {
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Target skill.\n---\n# Body\n"), 0o644))
	other := t.TempDir()
	otherSkill := filepath.Join(other, "skills", "my-skill")
	otherEval := filepath.Join(other, "evals", "my-skill")
	require.NoError(t, os.MkdirAll(otherSkill, 0o755))
	require.NoError(t, os.MkdirAll(otherEval, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Other skill.\n---\n# Body\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(otherEval, "eval.yaml"), []byte("name: unrelated\n"), 0o644))
	t.Chdir(other)
	report, err := checkReadiness(target, nil)
	require.NoError(t, err)
	require.False(t, report.hasEval)
	require.Empty(t, report.evalPath)
}

func TestCheckReadiness_InvalidTargetConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Target skill.\n---\n# Body\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("unknown_field: true\n"), 0o644))
	_, err := checkReadiness(dir, nil)
	require.ErrorContains(t, err, "finding evaluation suite")
}

func TestCheckCommand_ExplicitCompiledSkillPath(t *testing.T) {
	for _, location := range []string{"evals/eval.yaml", "eval.yaml"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "skills", "my-skill")
			compiled := filepath.Join(source, ".apm", "skills", "my-skill")
			require.NoError(t, os.MkdirAll(compiled, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(compiled, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Compiled skill.\n---\n# Body\n"), 0o644))
			evalPath := filepath.Join(source, filepath.FromSlash(location))
			require.NoError(t, os.MkdirAll(filepath.Dir(evalPath), 0o755))
			require.NoError(t, os.WriteFile(evalPath, []byte("name: source\nskill: my-skill\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(compiled, "eval.yaml"), []byte("name: compiled\n"), 0o644))
			t.Chdir(t.TempDir())
			for _, target := range []string{compiled, filepath.Join(compiled, "SKILL.md")} {
				cmd := newCheckCommand()
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				cmd.SetArgs([]string{target, "--format", "json"})
				require.NoError(t, cmd.Execute())
				var report checkJSONReport
				require.NoError(t, json.Unmarshal(output.Bytes(), &report))
				require.Len(t, report.Skills, 1)
				require.True(t, report.Skills[0].Eval.Found)
				require.Equal(t, evalPath, report.Skills[0].Eval.Path)
			}
		})
	}
}

func TestCheckCommand_WorkspaceMultiSkill(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")

	for _, name := range []string{"skill-one", "skill-two"} {
		skillDir := filepath.Join(skillsDir, name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		content := "---\nname: " + name + "\ndescription: \"A test skill description.\"\n---\n# Body\n"
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
	}
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil) // no args — multi-skill workspace detection

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "=== skill-one ===")
	assert.Contains(t, result, "=== skill-two ===")
	assert.Contains(t, result, "CHECK SUMMARY")
}

func TestCheckCommand_WorkspaceByName(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")

	for _, name := range []string{"named-skill", "other-skill"} {
		skillDir := filepath.Join(skillsDir, name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		content := "---\nname: " + name + "\ndescription: \"A test skill description.\"\n---\n# Body\n"
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
	}
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"named-skill"}) // skill name

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "named-skill")
	// Should NOT contain the other skill or multi-skill summary
	assert.NotContains(t, result, "CHECK SUMMARY")
}

func TestCheckCommand_ExplicitPathStillWorks(t *testing.T) {
	// Backward compatibility: explicit path arg works as before
	tmpDir := t.TempDir()
	skillContent := `---
name: path-skill
description: A skill with explicit path for backward compat test.
---

# Path Skill

Body content.
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(skillContent), 0o644))

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{tmpDir}) // explicit path

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "path-skill")
	assert.Contains(t, result, "Compliance Score:")
}

func TestCheckCommand_WorkspaceWithSeparatedEval(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "eval-test")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := "---\nname: eval-test\ndescription: \"Testing eval detection.\"\n---\n# Body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	// Create separated eval: {root}/evals/{skill-name}/eval.yaml
	evalsDir := filepath.Join(dir, "evals", "eval-test")
	require.NoError(t, os.MkdirAll(evalsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalsDir, "eval.yaml"), []byte("name: test\n"), 0o644))
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"eval-test"})

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "Evaluation Suite: Found")
}

func TestCheckCommand_WorkspaceByNameNestedSkillWithSeparatedEval(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: skills/\n  evals: evals/\n"), 0o644))

	skillDir := filepath.Join(dir, "skills", "development", "skill-creator")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := "---\nname: skill-creator\ndescription: \"Testing nested eval detection.\"\n---\n# Body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	evalsDir := filepath.Join(dir, "evals", "skill-creator")
	require.NoError(t, os.MkdirAll(evalsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalsDir, "eval.yaml"), []byte("name: test\nskill: skill-creator\n"), 0o644))
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"skill-creator"})

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "skill-creator")
	assert.Contains(t, result, "Evaluation Suite: Found")
	assert.NotContains(t, result, "no SKILL.md found")
}

func TestCheckCommand_ExplicitNestedPathWithSeparatedEval(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: skills/\n  evals: evals/\n"), 0o644))

	skillDir := filepath.Join(dir, "skills", "development", "skill-creator")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := "---\nname: skill-creator\ndescription: \"Testing explicit nested path eval detection.\"\n---\n# Body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	evalsDir := filepath.Join(dir, "evals", "skill-creator")
	require.NoError(t, os.MkdirAll(evalsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalsDir, "eval.yaml"), []byte("name: test\nskill: skill-creator\n"), 0o644))
	t.Chdir(dir)

	cmd := newCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{filepath.Join("skills", "development", "skill-creator")})

	err := cmd.Execute()
	require.NoError(t, err)

	result := output.String()
	assert.Contains(t, result, "skill-creator")
	assert.Contains(t, result, "Evaluation Suite: Found")
}
