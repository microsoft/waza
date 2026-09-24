package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAgentFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestAugmentGradersFromAgent_AddsImplicitGrader(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "security.agent.md", `---
name: security-reviewer
description: Reviews code for vulnerabilities
tools:
  - search/codebase
  - filesystem/read
---

You are a security code reviewer.
`)

	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, agentPath)

	require.Len(t, result, 2, "should have original + injected grader")
	assert.Equal(t, models.GraderKindToolConstraint, result[1].Kind)
	assert.Equal(t, "agent_tools_implicit", result[1].Identifier)

	params, ok := result[1].Parameters.(models.ToolConstraintGraderParameters)
	require.True(t, ok, "parameters should be ToolConstraintGraderParameters")

	// Declared tools must be injected as AllowOnly (allow-list), not
	// ExpectTools. ExpectTools would force the agent to actually use every
	// listed tool, which is the bug fixed by issue #586.
	assert.Nil(t, params.ExpectTools, "must not inject expect_tools for .agent.md declarations")
	assert.Nil(t, params.RejectTools, "must not inject reject_tools for .agent.md declarations")
	require.NotNil(t, params.AllowOnly, "must inject allow_only for declared tools")
	require.Len(t, *params.AllowOnly, 2)
	assert.Equal(t, "search/codebase", (*params.AllowOnly)[0].Tool)
	assert.Equal(t, "filesystem/read", (*params.AllowOnly)[1].Tool)
}

// TestAugmentGradersFromAgent_EmptyToolsList covers `tools: []` — an explicit
// deny-all declaration. The grader must still be injected with a non-nil
// (but zero-length) AllowOnly so any tool call the agent makes is a policy
// violation.
func TestAugmentGradersFromAgent_EmptyToolsList(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "deny-all.agent.md", `---
name: deny-all
tools: []
---

I use no tools.
`)

	result := augmentGradersFromAgent(nil, agentPath)

	require.Len(t, result, 1, "explicit tools: [] must still inject a grader")
	params, ok := result[0].Parameters.(models.ToolConstraintGraderParameters)
	require.True(t, ok)
	require.NotNil(t, params.AllowOnly, "explicit tools: [] must produce non-nil AllowOnly")
	assert.Empty(t, *params.AllowOnly, "explicit tools: [] must produce zero-length AllowOnly")
}

func TestAugmentGradersFromAgent_SkipsWhenUserConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "my.agent.md", `---
name: my-agent
tools:
  - search/codebase
---

Body.
`)

	graders := []models.GraderConfig{
		{Kind: models.GraderKindToolConstraint, Identifier: "user_defined"},
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, agentPath)

	assert.Len(t, result, 2, "should not inject when user already has tool_constraint")
	assert.Equal(t, "user_defined", result[0].Identifier)
}

func TestAugmentGradersFromAgent_NoTools(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "bare.agent.md", `---
name: bare-agent
description: An agent with no tools
---

Just instructions, no tools.
`)

	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, agentPath)

	// Absent `tools:` key must NOT inject an implicit grader; the agent has
	// simply not opted in to tool constraints. This is different from
	// `tools: []`, which explicitly denies all tool use.
	assert.Len(t, result, 1, "absent tools: key must not inject a grader")
}

func TestAugmentGradersFromAgent_NotAgentFile(t *testing.T) {
	tmpDir := t.TempDir()
	skillPath := filepath.Join(tmpDir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: my-skill
---
Body.
`), 0644))

	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, skillPath)

	assert.Len(t, result, 1, "should not inject for SKILL.md files")
}

func TestAugmentGradersFromAgent_MissingFile(t *testing.T) {
	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, "/nonexistent/path/ghost.agent.md")

	assert.Len(t, result, 1, "should not panic or inject for missing files")
}

func TestAugmentGradersFromAgent_EmptyPath(t *testing.T) {
	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, "")

	assert.Len(t, result, 1, "should return unchanged for empty path")
}

func TestResolveAgentPath_FindsAgent(t *testing.T) {
	tmpDir := t.TempDir()
	writeAgentFile(t, tmpDir, "reviewer.agent.md", `---
name: reviewer
---
Body.
`)

	result := resolveAgentPath([]string{tmpDir})
	assert.Equal(t, filepath.Join(tmpDir, "reviewer.agent.md"), result)
}

func TestResolveAgentPath_EmptyDirs(t *testing.T) {
	result := resolveAgentPath(nil)
	assert.Empty(t, result)
}

func TestResolveAgentPath_NoAgentFiles(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0644))

	result := resolveAgentPath([]string{tmpDir})
	assert.Empty(t, result)
}
