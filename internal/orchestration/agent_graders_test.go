package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadFMForTest loads the .agent.md frontmatter at path, returning nil on any
// error so tests can exercise augmentGradersFromAgent/resolveToolPolicy with
// the same nil-on-failure semantics the runner applies.
func loadFMForTest(path string) *skill.AgentFrontmatter {
	fm, _, err := skill.LoadAgentDefinition(path)
	if err != nil {
		return nil
	}
	return fm
}

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

	result := augmentGradersFromAgent(graders, agentPath, loadFMForTest(agentPath))

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

	result := augmentGradersFromAgent(nil, agentPath, loadFMForTest(agentPath))

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

	result := augmentGradersFromAgent(graders, agentPath, loadFMForTest(agentPath))

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

	result := augmentGradersFromAgent(graders, agentPath, loadFMForTest(agentPath))

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

	result := augmentGradersFromAgent(graders, skillPath, loadFMForTest(skillPath))

	assert.Len(t, result, 1, "should not inject for SKILL.md files")
}

func TestAugmentGradersFromAgent_MissingFile(t *testing.T) {
	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, "/nonexistent/path/ghost.agent.md", loadFMForTest("/nonexistent/path/ghost.agent.md"))

	assert.Len(t, result, 1, "should not panic or inject for missing files")
}

func TestAugmentGradersFromAgent_EmptyPath(t *testing.T) {
	graders := []models.GraderConfig{
		{Kind: models.GraderKindText, Identifier: "check_output"},
	}

	result := augmentGradersFromAgent(graders, "", loadFMForTest(""))

	assert.Len(t, result, 1, "should return unchanged for empty path")
}

func TestResolveToolPolicy_NoTools(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "bare.agent.md", `---
name: bare-agent
---
Body.
`)

	policy := resolveToolPolicy(loadFMForTest(agentPath))
	require.NotNil(t, policy)
	require.Equal(t, execution.ToolPolicyUnrestricted, policy.Mode)
}

func TestResolveToolPolicy_EmptyTools(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "deny-all.agent.md", `---
name: deny-all
tools: []
---
Body.
`)

	policy := resolveToolPolicy(loadFMForTest(agentPath))
	require.NotNil(t, policy)
	require.Equal(t, execution.ToolPolicyDenyAll, policy.Mode)
	require.False(t, policy.IsAllowed("bash"))
}

func TestResolveToolPolicy_PopulatedTools(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := writeAgentFile(t, tmpDir, "reader.agent.md", `---
name: reader
tools:
  - read
  - readFile
---
Body.
`)

	policy := resolveToolPolicy(loadFMForTest(agentPath))
	require.NotNil(t, policy)
	require.Equal(t, execution.ToolPolicyAllowList, policy.Mode)
	require.True(t, policy.IsAllowed("read"))
	require.True(t, policy.IsAllowed("readFile"))
	require.False(t, policy.IsAllowed("bash"))
}

func TestResolveToolPolicy_NotAgentFileOrMissing(t *testing.T) {
	require.Nil(t, resolveToolPolicy(loadFMForTest("")))
	require.Nil(t, resolveToolPolicy(loadFMForTest("/nonexistent/path/ghost.agent.md")))

	tmpDir := t.TempDir()
	skillPath := filepath.Join(tmpDir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: my-skill\n---\nBody.\n"), 0644))
	require.Equal(t, execution.ToolPolicyUnrestricted, resolveToolPolicy(loadFMForTest(skillPath)).Mode)
}

func TestBuildExecutionRequest_ResolvesPolicyAfterPathsChange(t *testing.T) {
	tmpDir := t.TempDir()
	writeAgentFile(t, tmpDir, "reviewer.agent.md", `---
name: reviewer
tools:
  - read
---
Body.
`)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "task.yaml"), []byte(
		"name: test-task\ninputs:\n  prompt: hello\nid: test-task\n"), 0644))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "test-eval"},
		SkillName:    "reviewer",
		Config: models.Config{
			EngineType: "mock",
			ModelID:    "gpt-4",
			SkillPaths: []string{tmpDir},
		},
		Tasks: []string{"task.yaml"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	engine := execution.NewMockEngine("gpt-4")
	runner := NewEvalRunner(cfg, engine)

	req, err := runner.buildExecutionRequest(&models.TestCase{})
	require.NoError(t, err)
	require.NotNil(t, req.ToolPolicy)
	require.Equal(t, execution.ToolPolicyAllowList, req.ToolPolicy.Mode)

	// Simulate PASS 2 of runBaselineComparison: SkillPaths cleared, so no
	// agentPath is found this time.
	spec.Config.SkillPaths = []string{}
	req, err = runner.buildExecutionRequest(&models.TestCase{})
	require.NoError(t, err)
	require.Nil(t, req.ToolPolicy, "stale tool policy from the prior pass must not persist")
}

// TestRunNormalBenchmark_MalformedAgentFilePropagatesError verifies that a
// malformed/unreadable .agent.md fails the run instead of silently falling
// back to an unrestricted tool policy.
func TestRunNormalBenchmark_MalformedAgentFilePropagatesError(t *testing.T) {
	tmpDir := t.TempDir()
	// Invalid YAML frontmatter (unterminated) so skill.LoadAgentDefinition errors.
	writeAgentFile(t, tmpDir, "broken.agent.md", "---\nname: [unterminated\nBody.\n")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "task.yaml"), []byte(
		"name: test-task\ninputs:\n  prompt: hello\nid: test-task\n"), 0644))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "test-eval"},
		SkillName:    "test-skill",
		Config: models.Config{
			EngineType: "mock",
			ModelID:    "gpt-4",
			SkillPaths: []string{tmpDir},
		},
		Tasks: []string{"task.yaml"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	engine := execution.NewMockEngine("gpt-4")
	runner := NewEvalRunner(cfg, engine)

	req, err := runner.buildExecutionRequest(&models.TestCase{})
	require.Error(t, err)
	require.Nil(t, req)
}

func TestBuildExecutionRequest_SelectedAgentPolicy(t *testing.T) {
	for _, scenario := range []string{"selected", "override", "disabled", "skill-priority", "nested", "nested-with-root-agent", "unrestricted", "same-directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			other := filepath.Join(root, "other")
			target := filepath.Join(root, "target")
			require.NoError(t, os.MkdirAll(other, 0755))
			require.NoError(t, os.MkdirAll(target, 0755))
			writeAgentFile(t, other, "other.agent.md", "---\nname: other\ntools: []\n---\n")
			tools := "tools: [fileRead]\n"
			if scenario == "unrestricted" {
				tools = ""
			}
			writeAgentFile(t, target, "target.agent.md", "---\nname: target\n"+tools+"---\n")
			spec := &models.EvalSpec{SkillName: "target", Config: models.Config{SkillPaths: []string{other, target}}}
			tc := &models.TestCase{}
			switch scenario {
			case "override":
				spec.Config.SkillPaths = []string{other}
				tc.SkillPaths = []string{target}
			case "disabled":
				spec.Config.DisabledSkills = []string{"*"}
			case "skill-priority":
				require.NoError(t, os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: target\n---\n"), 0644))
			case "nested":
				spec.Config.SkillPaths = []string{root}
			case "nested-with-root-agent":
				writeAgentFile(t, root, "unrelated.agent.md", "---\nname: unrelated\ntools: []\n---\n")
				spec.Config.SkillPaths = []string{root}
			case "same-directory":
				writeAgentFile(t, target, "aaa.agent.md", "---\nname: another\ntools: []\n---\n")
				spec.Config.SkillPaths = []string{target}
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec), execution.NewMockEngine("mock"))
			req, err := runner.buildExecutionRequest(tc)
			require.NoError(t, err)
			switch scenario {
			case "disabled", "skill-priority":
				require.Nil(t, req.ToolPolicy)
			case "unrestricted":
				require.Equal(t, execution.ToolPolicyUnrestricted, req.ToolPolicy.Mode)
			default:
				require.Equal(t, execution.ToolPolicyAllowList, req.ToolPolicy.Mode)
				require.True(t, req.ToolPolicy.IsAllowed("view"))
				require.False(t, req.ToolPolicy.IsAllowed("bash"))
			}
		})
	}
}
