package orchestration

import (
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
)

// augmentGradersFromAgent injects an implicit tool_constraint grader when the
// target is an .agent.md whose frontmatter declares a `tools:` key. Skips
// injection if the eval already defines a tool_constraint grader (user opt-out).
//
// The declared tool list is translated into an ALLOW-LIST policy, not an
// expectation list: undeclared tools observed in the session cause the grader
// to fail, but the agent is NOT required to actually use every declared tool.
// This matches the .agent.md contract described in guides/custom-agents.mdx
// and issue #586, and is deliberately different from `expect_tools` (which is
// a lower bound: every listed tool must be exercised).
//
// Tri-state semantics of `fm.Tools`:
//
//   - nil (key absent): no allow-list is injected.
//   - explicit empty (`tools: []`): a deny-all allow-list is injected — any
//     tool call the agent makes will fail the grader.
//   - populated: entries are used as an exact-match (case-insensitive)
//     allow-list. Tool names are NOT treated as regexes.
func augmentGradersFromAgent(graders []models.GraderConfig, agentPath string) []models.GraderConfig {
	if agentPath == "" || !skill.IsAgentFile(agentPath) {
		return graders
	}

	// Skip if user already declared a tool_constraint grader
	for _, g := range graders {
		if g.Kind == models.GraderKindToolConstraint {
			return graders
		}
	}

	fm, _, err := skill.LoadAgentDefinition(agentPath)
	if err != nil || fm == nil || fm.Tools == nil {
		return graders
	}

	// fm.Tools is non-nil here (possibly empty).
	declared := *fm.Tools
	allowOnly := make([]models.ToolSpecParameters, 0, len(declared))
	for _, t := range declared {
		allowOnly = append(allowOnly, models.ToolSpecParameters{Tool: t})
	}

	implicit := models.GraderConfig{
		Kind:       models.GraderKindToolConstraint,
		Identifier: "agent_tools_implicit",
		Parameters: models.ToolConstraintGraderParameters{
			AllowOnly: &allowOnly,
		},
	}

	return append(graders, implicit)
}

// resolveAgentPath finds the first .agent.md file in the given skill directories.
// Returns empty string if no agent file is found.
func resolveAgentPath(skillPaths []string) string {
	for _, dir := range skillPaths {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && skill.IsAgentFile(entry.Name()) {
				return filepath.Join(dir, entry.Name())
			}
		}
	}
	return ""
}
