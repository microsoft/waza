package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCoverageReport_NoEvals(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeSkill(t, root, filepath.Join("skills", "beta"), "beta")

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)

	assert.Equal(t, 2, report.TotalSkills)
	assert.Equal(t, 0, report.Covered)
	assert.Equal(t, 0, report.Partial)
	assert.Equal(t, 2, report.Uncovered)
	assert.Equal(t, "❌ None", report.Skills[0].Coverage)
	assert.Equal(t, "❌ None", report.Skills[1].Coverage)
}

func TestBuildCoverageReport_PartialAndFull(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "partial-skill"), "partial-skill")
	writeSkill(t, root, filepath.Join(".github", "skills", "full-skill"), "full-skill")

	writeEval(t, root, filepath.Join("evals", "partial-skill", "eval.yaml"), `
skill: partial-skill
tasks:
  - tasks/*.yaml
graders:
  - type: prompt
    name: judge
`)
	writeEval(t, root, filepath.Join("custom", "full-skill", "eval.yaml"), `
skill: full-skill
tasks:
  - tasks/a.yaml
  - tasks/b.yaml
graders:
  - type: prompt
    name: judge
  - type: file
    name: files
`)

	report, err := buildCoverageReport(root, []string{"custom"})
	require.NoError(t, err)

	assert.Equal(t, 2, report.TotalSkills)
	assert.Equal(t, 1, report.Covered)
	assert.Equal(t, 1, report.Partial)
	assert.Equal(t, 0, report.Uncovered)
	assert.InDelta(t, 100.0, report.CoveragePct, 0.1)

	rows := map[string]coverageSkillRow{}
	for _, row := range report.Skills {
		rows[row.Skill] = row
	}

	assert.Equal(t, "⚠️ Partial", rows["partial-skill"].Coverage)
	assert.Equal(t, 1, rows["partial-skill"].Tasks)
	assert.Equal(t, []string{"prompt"}, rows["partial-skill"].Graders)

	assert.Equal(t, "✅ Full", rows["full-skill"].Coverage)
	assert.Equal(t, 2, rows["full-skill"].Tasks)
	assert.Equal(t, []string{"file", "prompt"}, rows["full-skill"].Graders)
}

func TestBuildCoverageReport_IncludesEvalYML(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeEval(t, root, filepath.Join("evals", "alpha", "eval.yml"), `
skill: alpha
tasks:
  - tasks/*.yaml
graders:
  - type: prompt
    name: judge
  - type: file
    name: files
`)

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, "✅ Full", report.Skills[0].Coverage)
}

func TestBuildCoverageReport_ReturnsParseErrors(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeEval(t, root, filepath.Join("evals", "alpha", "eval.yaml"), "skill: [bad")

	_, err := buildCoverageReport(root, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse 1 eval files")
}

func TestRenderCoverageMarkdown(t *testing.T) {
	report := &coverageReport{
		TotalSkills: 2,
		Skills: []coverageSkillRow{
			{Skill: "alpha", Tasks: 1, Graders: []string{"prompt"}, Coverage: "⚠️ Partial"},
			{Skill: "beta", Tasks: 2, Graders: []string{"file", "prompt"}, Coverage: "✅ Full"},
		},
	}

	var buf bytes.Buffer
	renderCoverageMarkdown(&buf, report)
	out := buf.String()

	assert.Contains(t, out, "📊 Eval Coverage Grid")
	assert.Contains(t, out, "| Skill | Tasks | Graders | Coverage |")
	assert.Contains(t, out, "| alpha | 1 | prompt | ⚠️ Partial |")
	assert.Contains(t, out, "| beta | 2 | file, prompt | ✅ Full |")
}

func TestRenderCoverageJSON(t *testing.T) {
	report := &coverageReport{
		TotalSkills: 1,
		Covered:     1,
		Partial:     0,
		Uncovered:   0,
		CoveragePct: 100,
		Skills: []coverageSkillRow{
			{Skill: "alpha", Tasks: 2, Graders: []string{"file", "prompt"}, Coverage: "✅ Full"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderCoverageJSON(&buf, report))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	assert.Equal(t, float64(1), decoded["total_skills"])
	assert.Contains(t, buf.String(), "\n  \"total_skills\"")
}

func TestCoverageCommand_UnsupportedFormat(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")

	cmd := newCoverageCommand()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{root, "--format", "xml"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported format "xml"`)
}

func TestRootCommand_HasCoverageSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "coverage" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'coverage' subcommand")
}

func writeSkill(t *testing.T, root, relDir, skillName string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := `---
name: ` + skillName + `
description: "test skill"
---
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func writeEval(t *testing.T, root, relPath, content string) {
	t.Helper()
	absPath := filepath.Join(root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))
}
