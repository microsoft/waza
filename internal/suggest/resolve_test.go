package suggest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- resolveSkillFile ---

func TestResolveSkillFile_EmptyPath(t *testing.T) {
	_, err := resolveSkillFile("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skill path is required")
}

func TestResolveSkillFile_WhitespacePath(t *testing.T) {
	_, err := resolveSkillFile("   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skill path is required")
}

func TestResolveSkillFile_NonexistentPath(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist", "skill")
	_, err := resolveSkillFile(nonexistent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestResolveSkillFile_DirectoryWithSKILLMD(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: test\n---\n# Test"), 0644))

	resolved, err := resolveSkillFile(dir)
	require.NoError(t, err)
	assert.Equal(t, skillPath, resolved)
}

func TestResolveSkillFile_DirectFileReference(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: test\n---\n# Test"), 0644))

	resolved, err := resolveSkillFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, skillPath, resolved)
}

func TestResolveSkillFile_DirectoryMissingSKILLMD(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveSkillFile(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SKILL.md found")
}

func TestResolveSkillFile_WrongFilename(t *testing.T) {
	dir := t.TempDir()
	wrongPath := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(wrongPath, []byte("# README"), 0644))

	_, err := resolveSkillFile(wrongPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected SKILL.md")
}

// --- loadSkill ---

func TestLoadSkill_ValidFile(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: test-skill\ndescription: \"A test skill.\"\n---\n# Test Skill\n\nDoes things.\n"
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	rawContent, sk, err := loadSkill(skillPath)
	require.NoError(t, err)
	assert.Equal(t, content, rawContent)
	assert.Equal(t, "test-skill", sk.Frontmatter.Name)
	assert.Equal(t, skillPath, sk.Path)
}

func TestLoadSkill_NonexistentFile(t *testing.T) {
	_, _, err := loadSkill("/nonexistent/SKILL.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading SKILL.md")
}

func TestLoadSkill_InvalidFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// Missing closing ---
	content := "---\nname: test\n# no closing delimiter"
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	_, _, err := loadSkill(skillPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing SKILL.md")
}

// --- buildPromptData ---

func TestBuildPromptData_ExtractsFields(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: build-prompt-skill\ndescription: \"A useful skill. USE FOR: summarize, explain. DO NOT USE FOR: coding, deploy.\"\n---\n# Overview\nThis skill summarizes docs.\n"
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	rawContent, sk, err := loadSkill(skillPath)
	require.NoError(t, err)

	data := buildPromptData(sk, rawContent)
	assert.Equal(t, "build-prompt-skill", data.SkillName)
	assert.NotEmpty(t, data.Description)
	assert.Contains(t, data.Triggers, "summarize")
	assert.Contains(t, data.AntiTriggers, "coding")
	assert.NotEmpty(t, data.ContentSummary)
	assert.Contains(t, data.GraderTypes, "code")
	assert.Equal(t, rawContent, data.SkillContent)
}

func TestBuildPromptData_FallbackSkillName(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "my-cool-skill")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	content := "---\ndescription: \"Some skill.\"\n---\n# Content\n"
	skillPath := filepath.Join(subDir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	rawContent, sk, err := loadSkill(skillPath)
	require.NoError(t, err)

	data := buildPromptData(sk, rawContent)
	// Name not set in frontmatter, should fall back to parent dir name
	assert.Equal(t, "my-cool-skill", data.SkillName)
}

func TestBuildPromptData_NoTriggers(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: simple-skill\ndescription: \"A plain skill with no trigger phrases.\"\n---\n# Simple\n"
	skillPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	rawContent, sk, err := loadSkill(skillPath)
	require.NoError(t, err)

	data := buildPromptData(sk, rawContent)
	assert.Equal(t, "none", data.Triggers)
	assert.Equal(t, "none", data.AntiTriggers)
}

// --- WriteToDir edge cases ---

func TestWriteToDir_EmptyTaskPath(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "", Content: "id: auto\nname: Auto\ninputs:\n  prompt: hi"},
		},
	}

	dir := t.TempDir()
	written, err := s.WriteToDir(dir)
	require.NoError(t, err)
	assert.Len(t, written, 2) // eval.yaml + auto-named task
}

func TestWriteToDir_EmptyFixturePath(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Fixtures: []GeneratedFile{
			{Path: "", Content: "fixture content"},
		},
	}

	dir := t.TempDir()
	written, err := s.WriteToDir(dir)
	require.NoError(t, err)
	assert.Len(t, written, 2) // eval.yaml + auto-named fixture
}

func TestWriteToDir_AbsolutePathRejected(t *testing.T) {
	absPath := "/etc/evil.yaml"
	if runtime.GOOS == "windows" {
		absPath = `C:\evil.yaml`
	}
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: absPath, Content: "id: x\nname: X\ninputs:\n  prompt: hi"},
		},
	}

	dir := t.TempDir()
	_, err := s.WriteToDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid generated path")
}

func TestWriteToDir_TraversalPathRejected(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Fixtures: []GeneratedFile{
			{Path: "../../etc/passwd", Content: "evil"},
		},
	}

	dir := t.TempDir()
	_, err := s.WriteToDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid generated path")
}

func TestWriteToDir_InvalidEvalYAML(t *testing.T) {
	s := &Suggestion{
		EvalYAML: "this is not valid eval yaml",
	}

	dir := t.TempDir()
	_, err := s.WriteToDir(dir)
	require.Error(t, err)
}

func validEvalYAML() string {
	return `name: test-eval
description: test
skill: sample
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  parallel: false
  executor: mock
  model: test
graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 0"
metrics:
  - name: completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"`
}
