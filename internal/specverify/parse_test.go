package specverify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSkillFileExtractsRequirementsWithSpans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := `---
name: pr-summarizer
description: |
  Summarize PR diffs.
  USE FOR: summarize a PR diff, summarize PR discussion.
  DO NOT USE FOR: code review security PRs (use security-review).
---

# PR Summarizer

## Parameters
- repository: GitHub repository URL
- pr_number: Pull request number
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	spec, err := ParseSkillFile(path)
	require.NoError(t, err)

	byID := map[string]Requirement{}
	for _, req := range spec.Requirements {
		byID[req.ID] = req
	}

	assert.Equal(t, "pr-summarizer", spec.SkillName)
	assert.Equal(t, "Summarize PR diffs", byID["req-description-001"].Text)
	assert.Equal(t, RequirementUse, byID["req-use-001"].Kind)
	assert.Equal(t, "summarize a PR diff", byID["req-use-001"].Text)
	assert.Equal(t, 5, byID["req-use-001"].Source.StartLine)
	assert.Equal(t, RequirementDont, byID["req-dont-001"].Kind)
	assert.Equal(t, "code review security PRs", byID["req-dont-001"].Text)
	assert.Equal(t, 6, byID["req-dont-001"].Source.StartLine)
	assert.Equal(t, RequirementParameter, byID["req-param-001"].Kind)
	assert.Equal(t, "repository: GitHub repository URL", byID["req-param-001"].Text)
	assert.Equal(t, path, byID["req-param-001"].Source.File)
	assert.Equal(t, 12, byID["req-param-001"].Source.StartLine)
	assert.Equal(t, "pr_number: Pull request number", byID["req-param-002"].Text)
}

func TestParseSkillFileCleansBacktickParameterNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := `---
name: deployer
description: Deploy applications.
---

## Parameters
- ` + "`environment`" + `: Target deployment environment
- ` + "`region`" + ` - Azure region
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	spec, err := ParseSkillFile(path)
	require.NoError(t, err)

	require.Len(t, spec.Requirements, 3)
	assert.Equal(t, "environment: Target deployment environment", spec.Requirements[1].Text)
	assert.Equal(t, "region: Azure region", spec.Requirements[2].Text)
}

func TestParseSkillFileExistingCorpus(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "examples", "code-explainer", "SKILL.md"),
		filepath.Join("..", "..", "skills", "waza", "SKILL.md"),
		filepath.Join("..", "..", ".github", "skills", "azd-publish", "SKILL.md"),
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("corpus skill not present: %s", path)
			}
			spec, err := ParseSkillFile(path)
			require.NoError(t, err)
			require.NotEmpty(t, spec.Requirements)
			for _, req := range spec.Requirements {
				assert.NotEmpty(t, req.ID)
				assert.NotEmpty(t, req.Text)
				assert.Greater(t, req.Source.StartLine, 0)
				assert.GreaterOrEqual(t, req.Source.EndLine, req.Source.StartLine)
			}
		})
	}
}
