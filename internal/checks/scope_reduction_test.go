package checks

import (
	"testing"

	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeSkillWithContent(raw string) skill.Skill {
	sk := skill.Skill{RawContent: raw}
	// Parse body from raw content (mirrors skillBodyContent logic).
	sk.Body = skillBodyContent(sk)
	return sk
}

func TestScopeReductionChecker_FullScope(t *testing.T) {
	content := `---
name: multi-workflow
description: |
  USE FOR: "deploy apps", "run tests", "lint code", "build containers"
---

## Deployment Workflow

1. Build the container
2. Push to registry
3. Deploy to cluster

## Testing Workflow

1. Run unit tests
2. Run integration tests

## Linting Workflow

1. Run eslint
2. Run prettier
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	assert.Equal(t, "scope-reduction", result.Name)

	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, StatusOK, data.Status)
	assert.Equal(t, 4, data.UseForCount)
	assert.Equal(t, 3, data.HeadingCount)
	assert.Equal(t, 3, data.StepSequences)
	assert.GreaterOrEqual(t, data.TotalCapabilities, 2)
}

func TestScopeReductionChecker_ReducedScope(t *testing.T) {
	content := `---
name: compressed-skill
description: A minimal skill.
---

Some instructions here without headings or steps.
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Contains(t, result.Summary, "Low capability scope")
	assert.Contains(t, result.Summary, "token-limit compression loss")

	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, StatusWarning, data.Status)
	assert.Equal(t, 0, data.TotalCapabilities)
}

func TestScopeReductionChecker_SingleHeading(t *testing.T) {
	content := `---
name: single-heading
description: One workflow skill.
---

## Only Workflow

Do something here.
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.False(t, result.Passed, "1 heading < default threshold of 2")

	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, StatusWarning, data.Status)
	assert.Equal(t, 1, data.HeadingCount)
	assert.Equal(t, 1, data.TotalCapabilities)
}

func TestScopeReductionChecker_UseForOnly(t *testing.T) {
	content := `---
name: trigger-rich
description: |
  USE FOR: "analyze code", "review PRs", "generate docs"
---

This skill does many things but has no headings or steps.
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 3, data.UseForCount)
	assert.Equal(t, 3, data.TotalCapabilities)
}

func TestScopeReductionChecker_StepsOnly(t *testing.T) {
	content := `---
name: steps-skill
description: Multi-procedure skill.
---

First procedure:

1. Do X
2. Do Y

Second procedure:

1. Do A
2. Do B
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 2, data.StepSequences)
	assert.Equal(t, 2, data.TotalCapabilities)
}

func TestScopeReductionChecker_CustomThreshold(t *testing.T) {
	content := `---
name: two-heading
description: Skill with two headings.
---

## Workflow A

Steps here.

## Workflow B

More steps.
`
	sk := makeSkillWithContent(content)

	// With threshold of 3, two headings is not enough.
	checker := &ScopeReductionChecker{MinCapabilities: 3}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.False(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 2, data.TotalCapabilities)
	assert.Equal(t, 3, data.Threshold)
}

func TestScopeReductionChecker_EmptyBody(t *testing.T) {
	content := `---
name: empty-body
description: Just a description.
---
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.False(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, StatusWarning, data.Status)
	assert.Equal(t, 0, data.TotalCapabilities)
}

func TestScopeReductionChecker_NoFrontmatter(t *testing.T) {
	content := `# Plain Markdown

## Section A

1. Step one
2. Step two

## Section B

Some content.
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 2, data.HeadingCount)
}

func TestScopeReductionChecker_UseForInBody(t *testing.T) {
	content := `---
name: body-use-for
description: A skill.
---

USE FOR: "task A", "task B", "task C"
DO NOT USE FOR: "task D"
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 3, data.UseForCount, "USE FOR items counted, DO NOT USE FOR excluded")
}

func TestScopeReductionChecker_MaxSignalWins(t *testing.T) {
	// 1 USE FOR item, 3 headings, 0 steps → total should be 3
	content := `---
name: mixed
description: |
  USE FOR: "one thing"
---

## A

Content.

## B

Content.

## C

Content.
`
	sk := makeSkillWithContent(content)
	checker := &ScopeReductionChecker{}
	result, err := checker.Check(sk)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	data, ok := result.Data.(*ScopeReductionData)
	require.True(t, ok)
	assert.Equal(t, 1, data.UseForCount)
	assert.Equal(t, 3, data.HeadingCount)
	assert.Equal(t, 3, data.TotalCapabilities, "max of signals wins")
}

func TestCountUseForItems(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "single line",
			input: `USE FOR: "a", "b", "c"`,
			want:  3,
		},
		{
			name:  "case insensitive",
			input: `use for: alpha, beta`,
			want:  2,
		},
		{
			name:  "multiple lines",
			input: "USE FOR: a, b\nUSE FOR: c",
			want:  3,
		},
		{
			name:  "no match",
			input: "DO NOT USE FOR: x, y",
			want:  0,
		},
		{
			name:  "empty after colon",
			input: "USE FOR:  ",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countUseForItems(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMaxInt(t *testing.T) {
	assert.Equal(t, 3, maxInt(1, 2, 3))
	assert.Equal(t, 3, maxInt(3, 2, 1))
	assert.Equal(t, 3, maxInt(1, 3, 2))
	assert.Equal(t, 0, maxInt(0, 0, 0))
	assert.Equal(t, 5, maxInt(5, 5, 5))
}
