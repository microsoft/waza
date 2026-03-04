package checks

import (
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

func makeSkill(name, description string, raw map[string]any, path string) skill.Skill {
	sk := skill.Skill{
		Frontmatter:    skill.Frontmatter{Name: name, Description: description},
		FrontmatterRaw: raw,
		Path:           path,
	}
	return sk
}

func TestSpecFrontmatterChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
		status CheckStatus
	}{
		{
			name:   "valid frontmatter",
			sk:     makeSkill("my-skill", "A skill that does things", map[string]any{"name": "my-skill", "description": "A skill"}, ""),
			passed: true,
			status: StatusOK,
		},
		{
			name:   "missing frontmatter",
			sk:     skill.Skill{},
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "missing name",
			sk:     makeSkill("", "A description", map[string]any{"description": "A desc"}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "missing description",
			sk:     makeSkill("my-skill", "", map[string]any{"name": "my-skill"}, ""),
			passed: false,
			status: StatusWarning,
		},
	}
	checker := &SpecFrontmatterChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ScoreCheckData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

func TestSpecAllowedFieldsChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
	}{
		{
			name:   "all allowed fields",
			sk:     makeSkill("s", "d", map[string]any{"name": "s", "description": "d", "license": "MIT"}, ""),
			passed: true,
		},
		{
			name:   "unknown field",
			sk:     makeSkill("s", "d", map[string]any{"name": "s", "author": "me"}, ""),
			passed: false,
		},
		{
			name:   "no frontmatter",
			sk:     skill.Skill{},
			passed: true,
		},
	}
	checker := &SpecAllowedFieldsChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
		})
	}
}

func TestSpecNameChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
	}{
		{
			name:   "valid name",
			sk:     makeSkill("my-skill-2", "desc", map[string]any{}, ""),
			passed: true,
		},
		{
			name:   "empty name (defers to frontmatter check)",
			sk:     makeSkill("", "desc", map[string]any{}, ""),
			passed: true,
		},
		{
			name:   "too long",
			sk:     makeSkill(strings.Repeat("a", 65), "desc", map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "leading hyphen",
			sk:     makeSkill("-bad", "desc", map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "trailing hyphen",
			sk:     makeSkill("bad-", "desc", map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "consecutive hyphens",
			sk:     makeSkill("bad--name", "desc", map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "uppercase chars",
			sk:     makeSkill("Bad-Name", "desc", map[string]any{}, ""),
			passed: false,
		},
	}
	checker := &SpecNameChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed, "summary: %s", result.Summary)
		})
	}
}

func TestSpecDirMatchChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
	}{
		{
			name:   "matching",
			sk:     makeSkill("my-skill", "desc", map[string]any{}, "/skills/my-skill/SKILL.md"),
			passed: true,
		},
		{
			name:   "not matching",
			sk:     makeSkill("my-skill", "desc", map[string]any{}, "/skills/other-name/SKILL.md"),
			passed: false,
		},
		{
			name:   "no path",
			sk:     makeSkill("my-skill", "desc", map[string]any{}, ""),
			passed: true,
		},
	}
	checker := &SpecDirMatchChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
		})
	}
}

func TestSpecDescriptionChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
	}{
		{
			name:   "valid description",
			sk:     makeSkill("s", "A valid description", map[string]any{}, ""),
			passed: true,
		},
		{
			name:   "empty description",
			sk:     makeSkill("s", "", map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "over 1024 chars",
			sk:     makeSkill("s", strings.Repeat("x", 1025), map[string]any{}, ""),
			passed: false,
		},
		{
			name:   "exactly 1024 chars",
			sk:     makeSkill("s", strings.Repeat("x", 1024), map[string]any{}, ""),
			passed: true,
		},
	}
	checker := &SpecDescriptionChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
		})
	}
}

func TestSpecCompatibilityChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
	}{
		{
			name:   "no compatibility field",
			sk:     makeSkill("s", "d", map[string]any{}, ""),
			passed: true,
		},
		{
			name:   "valid compatibility map",
			sk:     makeSkill("s", "d", map[string]any{"compatibility": map[string]any{"editors": "vscode"}}, ""),
			passed: true,
		},
		{
			name:   "compatibility is a string (not a map)",
			sk:     makeSkill("s", "d", map[string]any{"compatibility": "Works with copilot"}, ""),
			passed: false,
		},
		{
			name:   "compatibility map with non-string value",
			sk:     makeSkill("s", "d", map[string]any{"compatibility": map[string]any{"editors": 42}}, ""),
			passed: false,
		},
		{
			name:   "no frontmatter",
			sk:     skill.Skill{},
			passed: true,
		},
	}
	checker := &SpecCompatibilityChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
		})
	}
}

func TestSpecLicenseChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
		status CheckStatus
	}{
		{
			name:   "license present",
			sk:     makeSkill("s", "d", map[string]any{"license": "MIT"}, ""),
			passed: true,
			status: StatusOptimal,
		},
		{
			name:   "license missing",
			sk:     makeSkill("s", "d", map[string]any{}, ""),
			passed: true,
			status: StatusWarning,
		},
		{
			name:   "license empty string",
			sk:     makeSkill("s", "d", map[string]any{"license": ""}, ""),
			passed: true,
			status: StatusWarning,
		},
		{
			name:   "no frontmatter",
			sk:     skill.Skill{},
			passed: true,
			status: StatusWarning,
		},
	}
	checker := &SpecLicenseChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ScoreCheckData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

func TestSpecVersionChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
		status CheckStatus
	}{
		{
			name:   "version present",
			sk:     makeSkill("s", "d", map[string]any{"metadata": map[string]any{"version": "1.0.0"}}, ""),
			passed: true,
			status: StatusOptimal,
		},
		{
			name:   "no metadata",
			sk:     makeSkill("s", "d", map[string]any{}, ""),
			passed: true,
			status: StatusWarning,
		},
		{
			name:   "metadata without version",
			sk:     makeSkill("s", "d", map[string]any{"metadata": map[string]any{"author": "me"}}, ""),
			passed: true,
			status: StatusWarning,
		},
		{
			name:   "empty version",
			sk:     makeSkill("s", "d", map[string]any{"metadata": map[string]any{"version": ""}}, ""),
			passed: true,
			status: StatusWarning,
		},
		{
			name:   "metadata not a map",
			sk:     makeSkill("s", "d", map[string]any{"metadata": "string-value"}, ""),
			passed: true,
			status: StatusWarning,
		},
	}
	checker := &SpecVersionChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ScoreCheckData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

// Test #73: SpecSecurityChecker
func TestSpecSecurityChecker(t *testing.T) {
	tests := []struct {
		name   string
		sk     skill.Skill
		passed bool
		status CheckStatus
	}{
		{
			name:   "clean frontmatter",
			sk:     makeSkill("my-skill", "A description", map[string]any{}, ""),
			passed: true,
			status: StatusOK,
		},
		{
			name:   "XML angle brackets in description",
			sk:     makeSkill("my-skill", "Use <tag> for things", map[string]any{"description": "Use <tag> for things"}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "reserved prefix claude-",
			sk:     makeSkill("claude-helper", "A description", map[string]any{}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "reserved prefix anthropic-",
			sk:     makeSkill("anthropic-tool", "A description", map[string]any{}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "angle brackets in metadata",
			sk:     makeSkill("my-skill", "A description", map[string]any{"metadata": map[string]any{"author": "<script>"}}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "multiple violations",
			sk:     makeSkill("claude-tool", "Use <tag>", map[string]any{}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name: "angle brackets in nested slice",
			sk: makeSkill("my-skill", "A description", map[string]any{
				"allowed-tools": []any{
					"safe-tool",
					map[string]any{"name": "<danger>"},
				},
			}, ""),
			passed: false,
			status: StatusWarning,
		},
		{
			name:   "no frontmatter",
			sk:     skill.Skill{Frontmatter: skill.Frontmatter{Name: "test", Description: "test"}},
			passed: true,
			status: StatusOK,
		},
	}
	checker := &SpecSecurityChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := checker.Check(tt.sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ScoreCheckData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}
