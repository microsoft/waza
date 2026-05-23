package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSkillSystemMessage_NoSkills(t *testing.T) {
	msg := buildSkillSystemMessage([]string{t.TempDir()}, "", true)
	assert.Empty(t, msg)
}

func TestBuildSkillSystemMessage_DirectSkillMD(t *testing.T) {
	dir := t.TempDir()
	skillContent := "---\nname: test-skill\ndescription: A test skill\n---\n# Rules\nAlways say hello"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

	msg := buildSkillSystemMessage([]string{dir}, "test-skill", true)

	// Should include full skill content for target skill
	assert.Contains(t, msg, "<skill_context>")
	assert.Contains(t, msg, "Always say hello")
	assert.Contains(t, msg, "</skill_context>")

	// Should include summary
	assert.Contains(t, msg, "<available_skills>")
	assert.Contains(t, msg, "<name>test-skill</name>")
	assert.Contains(t, msg, "<description>A test skill</description>")
	assert.Contains(t, msg, "</available_skills>")
}

func TestBuildSkillSystemMessage_SuppressTargetSkillBody(t *testing.T) {
	dir := t.TempDir()
	skillContent := "---\nname: test-skill\ndescription: A test skill\n---\n# Rules\nAlways say hello"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

	msg := buildSkillSystemMessage([]string{dir}, "test-skill", false)

	assert.NotContains(t, msg, "<skill_context>")
	assert.NotContains(t, msg, "Always say hello")
	assert.Contains(t, msg, "<available_skills>")
	assert.Contains(t, msg, "<name>test-skill</name>")
	assert.Contains(t, msg, "<description>A test skill</description>")
}

func TestBuildSkillSystemMessage_NestedSkillMD(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0755))

	skillContent := "---\nname: nested-skill\ndescription: Nested\n---\nBody content"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644))

	msg := buildSkillSystemMessage([]string{root}, "nested-skill", true)

	assert.Contains(t, msg, "<skill_context>")
	assert.Contains(t, msg, "Body content")
	assert.Contains(t, msg, "<name>nested-skill</name>")
}

func TestBuildSkillSystemMessage_NonTargetSkillSummaryOnly(t *testing.T) {
	dir := t.TempDir()
	skillContent := "---\nname: other-skill\ndescription: Other\n---\n# Secret body"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

	// Target is different skill
	msg := buildSkillSystemMessage([]string{dir}, "my-target-skill", true)

	// Should have summary but NOT full body
	assert.Contains(t, msg, "<name>other-skill</name>")
	assert.NotContains(t, msg, "<skill_context>")
}

func TestBuildSkillSystemMessage_NoFrontmatter_UsesDirectoryName(t *testing.T) {
	dir := t.TempDir()
	skillContent := "# No frontmatter skill\nJust body."
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

	msg := buildSkillSystemMessage([]string{dir}, "", true)

	assert.Contains(t, msg, "<available_skills>")
	// Should use directory name as the skill name
	assert.Contains(t, msg, "<name>"+filepath.Base(dir)+"</name>")
}

func TestBuildSkillSystemMessage_SkipsHiddenAndVendor(t *testing.T) {
	root := t.TempDir()

	// Create hidden dir with skill (should be skipped)
	hidden := filepath.Join(root, ".hidden-skill")
	require.NoError(t, os.MkdirAll(hidden, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte("---\nname: hidden\n---\n"), 0644))

	// Create vendor dir with skill (should be skipped)
	vendor := filepath.Join(root, "vendor")
	require.NoError(t, os.MkdirAll(vendor, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, "SKILL.md"), []byte("---\nname: vendored\n---\n"), 0644))

	msg := buildSkillSystemMessage([]string{root}, "", true)
	assert.Empty(t, msg)
}

func TestBuildInstructionSystemMessage(t *testing.T) {
	msg := buildInstructionSystemMessage([]InstructionFile{
		{
			Path:    ".github/instructions/project.instructions.md",
			Content: []byte("Always use table-driven tests."),
		},
		{
			Path:    "docs/task.instructions.md",
			Content: []byte("Mention edge cases.\n"),
		},
	})

	assert.Contains(t, msg, "<instruction_files>")
	assert.Contains(t, msg, "<path>.github/instructions/project.instructions.md</path>")
	assert.Contains(t, msg, "Always use table-driven tests.")
	assert.Contains(t, msg, "<path>docs/task.instructions.md</path>")
	assert.Contains(t, msg, "Mention edge cases.")
	assert.Contains(t, msg, "</instruction_files>")
}

func TestBuildInstructionSystemMessage_NoInstructions(t *testing.T) {
	assert.Empty(t, buildInstructionSystemMessage(nil))
}

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		expectedName string
		expectedDesc string
	}{
		{
			name:         "valid frontmatter",
			content:      "---\nname: my-skill\ndescription: Does things\n---\nbody",
			expectedName: "my-skill",
			expectedDesc: "Does things",
		},
		{
			name:         "no frontmatter",
			content:      "# Just markdown",
			expectedName: "",
			expectedDesc: "",
		},
		{
			name:         "quoted values",
			content:      "---\nname: \"quoted-skill\"\ndescription: 'quoted desc'\n---\n",
			expectedName: "quoted-skill",
			expectedDesc: "quoted desc",
		},
		{
			name:         "unclosed frontmatter",
			content:      "---\nname: broken\n",
			expectedName: "",
			expectedDesc: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc := parseSkillFrontmatter(tt.content)
			assert.Equal(t, tt.expectedName, name)
			assert.Equal(t, tt.expectedDesc, desc)
		})
	}
}
