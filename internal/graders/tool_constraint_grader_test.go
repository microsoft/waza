package graders

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestToolConstraintGrader_RequiresAtLeastOneConstraint(t *testing.T) {
	_, err := NewToolConstraintGrader("empty", ToolConstraintGraderConfig{})
	if err == nil {
		t.Fatal("expected error for empty params")
	}
}

func TestToolConstraintGrader_ExpectTools_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}, {Tool: "edit"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "edit", "view"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "edit"}, {Name: "view"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}
}

func TestToolConstraintGrader_ExpectTools_Fail(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}, {Tool: "edit"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "view"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "view"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("expected fail, got pass")
	}
	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}
}

func TestToolConstraintGrader_RejectTools_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: "create_file"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "edit"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "edit"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
}

func TestToolConstraintGrader_RejectTools_Fail(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: "create_file"}, {Tool: "delete"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "create_file"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "create_file"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("expected fail, got pass")
	}
	// 1 of 2 reject tools was used → 1 pass, 1 fail → 0.5
	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}
}

func TestToolConstraintGrader_AllConstraints_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("full", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}, {Tool: "edit"}},
		RejectTools: []ToolSpec{{Tool: "create_file"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "edit", "view"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "edit"}, {Name: "view"}},
			Usage:     &models.UsageStats{Turns: 10, InputTokens: 4000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}
}

func TestToolConstraintGrader_AllConstraints_PartialFail(t *testing.T) {
	g, err := NewToolConstraintGrader("partial", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}, {Tool: "edit"}},
		RejectTools: []ToolSpec{{Tool: "create_file"}},
	})
	require.NoError(t, err)

	// bash used, edit missing, create_file used
	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "create_file"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "create_file"}},
			Usage:     &models.UsageStats{Turns: 10, InputTokens: 8000},
		},
	})
	require.NoError(t, err)
	require.False(t, result.Passed)

	// expect_tools: bash(pass) + edit(fail) = 2 checks
	// reject_tools: create_file(fail) = 1 check
	// total = 3 checks, 1 passed, score = 1/3
	require.Equal(t, 1.0/3.0, result.Score)
}

func TestToolConstraintGrader_NilSession(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}},
	})
	require.NoError(t, err)

	result, err := g.Grade(context.Background(), &Context{
		Session: nil,
	})
	require.NoError(t, err)
	require.False(t, result.Passed)
	require.Equal(t, 0.0, result.Score)
}

func TestToolConstraintGrader_Kind(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{
			{Tool: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if g.Kind() != models.GraderKindToolConstraint {
		t.Errorf("expected kind %s, got %s", models.GraderKindToolConstraint, g.Kind())
	}
	if g.Name() != "test" {
		t.Errorf("expected name 'test', got '%s'", g.Name())
	}
}

func TestToolConstraintGrader_EmptyToolsUsed(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}},
		RejectTools: []ToolSpec{{Tool: "delete"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{},
			ToolCalls: []models.ToolCall{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("expected fail — expected tool not found in empty list")
	}
	// expect: bash missing (fail), reject: delete not found (pass) → 1/2 = 0.5
	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}
}

// --- New tests for structured ToolSpec matching ---

func TestToolConstraintGrader_StructuredExpect_ToolNameOnly(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "edit"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "edit"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
}

func TestToolConstraintGrader_StructuredExpect_WithArgsPattern_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash", CommandPattern: `azd\s+up`}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "azd up --region eastus"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
}

func TestToolConstraintGrader_StructuredExpect_WithArgsPattern_Fail(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash", CommandPattern: `azd\s+up`}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "git status"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("expected fail — args don't match pattern")
	}
}

func TestToolConstraintGrader_StructuredReject_WithArgsPattern_Pass(t *testing.T) {
	// bash is used but NOT with rm -rf args, so should pass
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: "bash", CommandPattern: `rm\s+-rf`}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "ls -la"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Feedback)
	}
}

func TestToolConstraintGrader_StructuredReject_WithArgsPattern_Fail(t *testing.T) {
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: "bash", CommandPattern: `rm\s+-rf`}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "rm -rf /tmp/stuff"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Error("expected fail — rejected tool+args matched")
	}
}

func TestToolConstraintGrader_EmptyToolField(t *testing.T) {
	_, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: ""}},
	})
	if err == nil {
		t.Fatal("expected error for empty tool field")
	}
	if !strings.Contains(err.Error(), "config.expect_tools[0].tool: required non-empty string") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToolConstraintGrader_RegexToolName(t *testing.T) {
	// Regex match: "bash|shell" should match "bash"
	g, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "bash|shell"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{{Name: "bash"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Errorf("expected pass with regex tool name, got fail: %s", result.Feedback)
	}
}

func TestToolConstraintGrader_EmptyRejectToolField(t *testing.T) {
	_, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: ""}},
	})
	if err == nil {
		t.Fatal("expected error for empty reject tool field")
	}
	if !strings.Contains(err.Error(), "config.reject_tools[0].tool: required non-empty string") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToolConstraintGrader_InvalidToolRegex(t *testing.T) {
	_, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		ExpectTools: []ToolSpec{{Tool: "("}},
	})
	if err == nil {
		t.Fatal("expected error for invalid tool regex")
	}
	if !strings.Contains(err.Error(), "config.expect_tools[0].tool: invalid regex") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToolConstraintGrader_InvalidArgsPatternRegex(t *testing.T) {
	_, err := NewToolConstraintGrader("test", ToolConstraintGraderConfig{
		RejectTools: []ToolSpec{{Tool: "bash", CommandPattern: "("}},
	})
	if err == nil {
		t.Fatal("expected error for invalid command_pattern regex")
	}
	if !strings.Contains(err.Error(), "config.reject_tools[0].command_pattern: invalid regex") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
