package graders

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestToolConstraintGrader_RequiresAtLeastOneConstraint(t *testing.T) {
	_, err := NewToolConstraintGrader("empty", models.ToolConstraintGraderParameters{})
	if err == nil {
		t.Fatal("expected error for empty params")
	}
}

func TestToolConstraintGrader_ExpectTools_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}, {Tool: "edit"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}, {Tool: "edit"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: "create_file"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: "create_file"}, {Tool: "delete"}},
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
	g, err := NewToolConstraintGrader("full", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}, {Tool: "edit"}},
		RejectTools: []models.ToolSpecParameters{{Tool: "create_file"}},
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
	g, err := NewToolConstraintGrader("partial", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}, {Tool: "edit"}},
		RejectTools: []models.ToolSpecParameters{{Tool: "create_file"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}},
		RejectTools: []models.ToolSpecParameters{{Tool: "delete"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash"}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash", CommandPattern: `azd\s+up`}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash", CommandPattern: `azd\s+up`}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: "bash", CommandPattern: `rm\s+-rf`}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: "bash", CommandPattern: `rm\s+-rf`}},
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
	_, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: ""}},
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
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "bash|shell"}},
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
	_, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: ""}},
	})
	if err == nil {
		t.Fatal("expected error for empty reject tool field")
	}
	if !strings.Contains(err.Error(), "config.reject_tools[0].tool: required non-empty string") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToolConstraintGrader_InvalidToolRegex(t *testing.T) {
	_, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "("}},
	})
	if err == nil {
		t.Fatal("expected error for invalid tool regex")
	}
	if !strings.Contains(err.Error(), "config.expect_tools[0].tool: invalid regex") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToolConstraintGrader_InvalidArgsPatternRegex(t *testing.T) {
	_, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		RejectTools: []models.ToolSpecParameters{{Tool: "bash", CommandPattern: "("}},
	})
	if err == nil {
		t.Fatal("expected error for invalid command_pattern regex")
	}
	if !strings.Contains(err.Error(), "config.reject_tools[0].command_pattern: invalid regex") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Args: structured argument matchers on tool specs (issue #366).
// ---------------------------------------------------------------------------

func TestToolConstraintGrader_ExpectTools_ArgEquals_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "view",
			Args: map[string]argmatcher.Matcher{
				"path": {Kind: argmatcher.KindEquals, Equals: "/etc/hosts"},
			},
		}},
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"view"},
			ToolCalls: []models.ToolCall{
				{Name: "view", Arguments: models.ToolCallArgs{Path: "/etc/hosts"}},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "feedback: %s", res.Feedback)
}

func TestToolConstraintGrader_ExpectTools_ArgRegex_Fail(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "bash",
			Args: map[string]argmatcher.Matcher{
				"command": {Kind: argmatcher.KindRegex, Regex: `^npm test`},
			},
		}},
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "ls"}},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
}

func TestToolConstraintGrader_ExpectTools_ArgContains_Pass(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "bash",
			Args: map[string]argmatcher.Matcher{
				"command": {Kind: argmatcher.KindContains, Contains: "go test"},
			},
		}},
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "go test ./..."}},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "feedback: %s", res.Feedback)
}

func TestToolConstraintGrader_InvalidArgMatcher_ConstructError(t *testing.T) {
	_, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "bash",
			Args: map[string]argmatcher.Matcher{
				"command": {Kind: argmatcher.KindRegex, Regex: "["},
			},
		}},
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Regression tests for review feedback on issue #366.
// ---------------------------------------------------------------------------

// TestValidateToolSpecs_PersistsCompiledMatcher verifies that validateToolSpecs
// writes the Compile()-mutated matcher back into spec.Args. Because the map
// stores Matcher by value (not by pointer), Compile() mutates a local copy of
// each matcher during iteration; if the caller does not reassign the compiled
// copy into the map, every subsequent Match() call has to recompile the regex
// or JSON schema. The fix is `spec.Args[argName] = m` after Compile.
func TestValidateToolSpecs_PersistsCompiledMatcher(t *testing.T) {
	regexMatcher := argmatcher.Matcher{Kind: argmatcher.KindRegex, Regex: `^auth`}
	schemaMatcher := argmatcher.Matcher{
		Kind:       argmatcher.KindJSONSchema,
		JSONSchema: map[string]any{"type": "string"},
	}
	specs := []models.ToolSpecParameters{{
		Tool: "search",
		Args: map[string]argmatcher.Matcher{
			"query":  regexMatcher,
			"filter": schemaMatcher,
		},
	}}

	normalized, err := validateToolSpecs(specs, "expect_tools")
	require.NoError(t, err)
	require.Len(t, normalized, 1)

	for name, m := range normalized[0].Args {
		require.Truef(t, m.IsCompiled(),
			"matcher %q: Compile() side-effects were not persisted back into the map", name)
	}

	// Caller-side invariants should not have been mutated.
	require.False(t, regexMatcher.IsCompiled(), "input matcher should be unchanged")
	require.False(t, schemaMatcher.IsCompiled(), "input matcher should be unchanged")
}

// TestToolConstraintGrader_ExpectTools_MatchesExtraArgs verifies that
// argument matchers see engine-specific argument keys (e.g. MCP-style
// `query`/`limit`) that are not part of the fixed ToolCallArgs struct.
// `ToolCallArgs.Extra` (mapstructure ",remain") captures these so
// normalizeToolCallArgs can surface them.
func TestToolConstraintGrader_ExpectTools_MatchesExtraArgs(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "search",
			Args: map[string]argmatcher.Matcher{
				"query": {Kind: argmatcher.KindContains, Contains: "auth"},
				"limit": {
					Kind:  argmatcher.KindRange,
					Range: &argmatcher.RangeSpec{GTE: float64Ptr(1), LTE: float64Ptr(10)},
				},
			},
		}},
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"search"},
			ToolCalls: []models.ToolCall{
				{
					Name: "search",
					Arguments: models.ToolCallArgs{
						Extra: map[string]any{
							"query": "find auth bypass",
							"limit": 5,
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "feedback: %s", res.Feedback)
}

// TestToolConstraintGrader_ExpectTools_MissingExtraArg verifies the inverse:
// when the call lacks an MCP-style extra arg the matcher expects, the spec
// does not match (so the grader reports the expected tool as unused).
func TestToolConstraintGrader_ExpectTools_MissingExtraArg(t *testing.T) {
	g, err := NewToolConstraintGrader("test", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{
			Tool: "search",
			Args: map[string]argmatcher.Matcher{
				"query": {Kind: argmatcher.KindContains, Contains: "auth"},
			},
		}},
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"search"},
			ToolCalls: []models.ToolCall{
				// Note: no Extra map → `query` arg absent → spec does not match.
				{Name: "search", Arguments: models.ToolCallArgs{}},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
}

func float64Ptr(v float64) *float64 { return &v }
