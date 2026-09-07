package skill

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgentFrontmatter_Basic(t *testing.T) {
	content := `---
name: security-reviewer
description: Reviews code for security vulnerabilities
tools:
  - search/codebase
  - web/fetch
model: claude-sonnet-4
handoffs:
  - name: implementer
    description: Hands off implementation tasks
mcp-servers:
  - name: github
    config:
      owner: microsoft
agents:
  - security-scanner
---

You are a security reviewer. Analyze code for common vulnerabilities...
`

	fm, body, err := ParseAgentFrontmatter(content)
	require.NoError(t, err)

	assert.Equal(t, "security-reviewer", fm.Name)
	assert.Equal(t, "Reviews code for security vulnerabilities", fm.Description)
	require.NotNil(t, fm.Tools)
	assert.Equal(t, []string{"search/codebase", "web/fetch"}, *fm.Tools)
	assert.Equal(t, "claude-sonnet-4", fm.Model)

	require.Len(t, fm.Handoffs, 1)
	assert.Equal(t, "implementer", fm.Handoffs[0].Name)
	assert.Equal(t, "Hands off implementation tasks", fm.Handoffs[0].Description)

	require.Len(t, fm.MCPServers, 1)
	assert.Equal(t, "github", fm.MCPServers[0].Name)
	assert.Equal(t, "microsoft", fm.MCPServers[0].Config["owner"])

	assert.Equal(t, []string{"security-scanner"}, fm.Agents)
	assert.Contains(t, body, "You are a security reviewer")
}

func TestParseAgentFrontmatter_MinimalFields(t *testing.T) {
	content := `---
name: simple-agent
---

Just a simple agent body.
`

	fm, body, err := ParseAgentFrontmatter(content)
	require.NoError(t, err)

	assert.Equal(t, "simple-agent", fm.Name)
	assert.Empty(t, fm.Description)
	assert.Nil(t, fm.Tools)
	assert.Empty(t, fm.Model)
	assert.Nil(t, fm.Handoffs)
	assert.Nil(t, fm.MCPServers)
	assert.Nil(t, fm.Agents)
	assert.Contains(t, body, "Just a simple agent body")
}

func TestParseAgentFrontmatter_NoFrontmatter(t *testing.T) {
	content := `# No frontmatter here

Just a body with no YAML block.
`

	fm, body, err := ParseAgentFrontmatter(content)
	require.NoError(t, err)

	assert.Empty(t, fm.Name)
	assert.Empty(t, fm.Description)
	assert.Equal(t, content, body)
}

// TestParseAgentFrontmatter_ToolsTristate documents the difference between an
// absent `tools:` key and an explicit empty `tools: []` list. The distinction
// matters because the implicit allow-list grader (issue #586) treats absent
// as "no policy" and empty as "deny all tool use". Losing that distinction
// silently downgrades a deny-all agent policy to no policy at all.
func TestParseAgentFrontmatter_ToolsTristate(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		fm, _, err := ParseAgentFrontmatter(`---
name: no-tools-key
---
body`)
		require.NoError(t, err)
		assert.Nil(t, fm.Tools, "absent tools: key must decode as nil pointer")
	})

	t.Run("explicit empty", func(t *testing.T) {
		fm, _, err := ParseAgentFrontmatter(`---
name: deny-all
tools: []
---
body`)
		require.NoError(t, err)
		require.NotNil(t, fm.Tools, "explicit tools: [] must decode as non-nil pointer")
		assert.Empty(t, *fm.Tools, "explicit tools: [] must decode as zero-length slice")
	})

	t.Run("populated", func(t *testing.T) {
		fm, _, err := ParseAgentFrontmatter(`---
name: allow-two
tools:
  - alpha
  - beta
---
body`)
		require.NoError(t, err)
		require.NotNil(t, fm.Tools)
		assert.Equal(t, []string{"alpha", "beta"}, *fm.Tools)
	})
}

func TestIsAgentFile(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"security-reviewer.agent.md", true},
		{"my-agent.agent.md", true},
		{".agent.md", true},
		{"path/to/foo.agent.md", true},
		{"SKILL.md", false},
		{"agent.md", false},
		{"readme.md", false},
		{"foo.agent.txt", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsAgentFile(tt.filename))
		})
	}
}
