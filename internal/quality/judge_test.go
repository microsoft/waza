package quality

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type mockJudgeEngine struct {
	output string
	err    error
}

func (m *mockJudgeEngine) Initialize(context.Context) error { return nil }

func (m *mockJudgeEngine) Execute(_ context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &execution.ExecutionResponse{FinalOutput: m.output, Success: true}, nil
}

func (m *mockJudgeEngine) Shutdown(context.Context) error         { return nil }
func (m *mockJudgeEngine) SessionUsage(string) *models.UsageStats { return nil }

const validJudgeJSON = `{
  "dimensions": [
    {"name": "clarity", "score": 4, "feedback": "Instructions are clear and well-structured."},
    {"name": "completeness", "score": 3, "feedback": "Missing error handling guidance."},
    {"name": "trigger_precision", "score": 5, "feedback": "USE FOR and DO NOT USE FOR are precise."},
    {"name": "scope_coverage", "score": 4, "feedback": "Scope is well-defined."},
    {"name": "anti_patterns", "score": 3, "feedback": "Some steps could be more specific."}
  ],
  "overall_score": 3.8,
  "summary": "A solid skill with room for improvement in completeness and anti-pattern avoidance."
}`

func writeTestSkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := `---
name: test-skill
description: "A test skill. USE FOR: testing. DO NOT USE FOR: production."
---

# Test Skill

## Overview
This is a test skill for quality evaluation.

## Steps
1. Step one
2. Step two
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	return dir
}

func TestJudge_Success(t *testing.T) {
	skillDir := writeTestSkill(t)
	engine := &mockJudgeEngine{output: validJudgeJSON}

	resp, err := Judge(context.Background(), engine, JudgeOptions{
		SkillPath: skillDir,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Dimensions, 5)
	require.InDelta(t, 3.8, resp.OverallScore, 0.01)
	require.Contains(t, resp.Summary, "solid skill")
}

func TestJudge_EngineError(t *testing.T) {
	skillDir := writeTestSkill(t)
	engine := &mockJudgeEngine{err: fmt.Errorf("engine failure")}

	_, err := Judge(context.Background(), engine, JudgeOptions{
		SkillPath: skillDir,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "judge execution")
}

func TestJudge_InvalidJSON(t *testing.T) {
	skillDir := writeTestSkill(t)
	engine := &mockJudgeEngine{output: "not json at all"}

	_, err := Judge(context.Background(), engine, JudgeOptions{
		SkillPath: skillDir,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing judge response")
}

func TestJudge_MissingSkill(t *testing.T) {
	engine := &mockJudgeEngine{output: validJudgeJSON}

	_, err := Judge(context.Background(), engine, JudgeOptions{
		SkillPath: "/nonexistent/path",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "skill path does not exist")
}

func TestJudge_ValidationFailure(t *testing.T) {
	skillDir := writeTestSkill(t)
	// Return response with missing dimensions
	engine := &mockJudgeEngine{output: `{
		"dimensions": [{"name": "clarity", "score": 4, "feedback": "OK"}],
		"overall_score": 4.0,
		"summary": "Incomplete"
	}`}

	resp, err := Judge(context.Background(), engine, JudgeOptions{
		SkillPath: skillDir,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "judge response validation")
	require.NotNil(t, resp) // partial response still returned
}

func TestParseJudgeResponse_BareJSON(t *testing.T) {
	resp, err := ParseJudgeResponse(validJudgeJSON)
	require.NoError(t, err)
	require.Len(t, resp.Dimensions, 5)
	require.Equal(t, "clarity", resp.Dimensions[0].Name)
	require.Equal(t, 4, resp.Dimensions[0].Score)
}

func TestParseJudgeResponse_FencedJSON(t *testing.T) {
	input := "Here is my analysis:\n```json\n" + validJudgeJSON + "\n```\n"
	resp, err := ParseJudgeResponse(input)
	require.NoError(t, err)
	require.Len(t, resp.Dimensions, 5)
}

func TestParseJudgeResponse_EmptyOutput(t *testing.T) {
	_, err := ParseJudgeResponse("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no JSON found")
}

func TestParseJudgeResponse_InvalidJSON(t *testing.T) {
	_, err := ParseJudgeResponse("{invalid json}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid judge JSON")
}

func TestBuildJudgePrompt(t *testing.T) {
	rubric := DefaultRubric()
	prompt := BuildJudgePrompt("# My Skill\nDoes stuff.", rubric)

	require.Contains(t, prompt, "skill quality judge")
	require.Contains(t, prompt, "clarity")
	require.Contains(t, prompt, "completeness")
	require.Contains(t, prompt, "trigger_precision")
	require.Contains(t, prompt, "scope_coverage")
	require.Contains(t, prompt, "anti_patterns")
	require.Contains(t, prompt, "# My Skill")
	require.Contains(t, prompt, `"dimensions"`)
	require.Contains(t, prompt, `"overall_score"`)
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantJSON bool
	}{
		{"bare json", `{"key": "value"}`, true},
		{"fenced json", "```json\n{\"key\": \"value\"}\n```", true},
		{"with preamble", "Analysis:\n{\"key\": \"value\"}", true},
		{"empty", "", false},
		{"no json", "just text", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSON(tt.input)
			if tt.wantJSON {
				require.NotEmpty(t, result)
			} else {
				require.Empty(t, result)
			}
		})
	}
}

func TestResolveSkillFile(t *testing.T) {
	dir := writeTestSkill(t)

	// Directory resolves to SKILL.md
	resolved, err := resolveSkillFile(dir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "SKILL.md"), resolved)

	// Direct path to SKILL.md
	resolved, err = resolveSkillFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "SKILL.md"), resolved)

	// Empty path
	_, err = resolveSkillFile("")
	require.Error(t, err)

	// Non-SKILL.md file
	_, err = resolveSkillFile(filepath.Join(dir, "other.md"))
	require.Error(t, err)
}
