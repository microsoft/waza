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

// --- AllowOnly (issue #586) ------------------------------------------------

// allowOnlyParams is a small helper to keep the AllowOnly test cases readable.
func allowOnlyParams(specs ...models.ToolSpecParameters) models.ToolConstraintGraderParameters {
	list := append([]models.ToolSpecParameters(nil), specs...)
	return models.ToolConstraintGraderParameters{AllowOnly: &list}
}

func TestToolConstraintGrader_AllowOnly_AcceptsListedTools(t *testing.T) {
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "bash"}, models.ToolSpecParameters{Tool: "edit"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "edit"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "edit"}, {Name: "bash"}},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "all calls are in the allow-list; expected pass. feedback=%q", res.Feedback)
	require.Equal(t, 1.0, res.Score)
}

func TestToolConstraintGrader_AllowOnly_DoesNotRequireEveryDeclaredToolBeUsed(t *testing.T) {
	// Regression test for issue #586: declared tools form an upper bound
	// (allow-list), NOT a lower bound (expectation). Using only a subset of
	// declared tools must not fail the grader.
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(
			models.ToolSpecParameters{Tool: "bash"},
			models.ToolSpecParameters{Tool: "edit"},
			models.ToolSpecParameters{Tool: "view"},
		))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{{Name: "bash"}},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "using a subset of declared tools must pass; feedback=%q", res.Feedback)
	require.Equal(t, 1.0, res.Score)
}

func TestToolConstraintGrader_AllowOnly_FailsOnUndeclaredTool(t *testing.T) {
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "bash"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash", "curl"},
			ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "curl"}, {Name: "bash"}},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed, "an undeclared tool must fail the grader")
	require.Contains(t, res.Feedback, "curl")
	require.Contains(t, res.Feedback, "allow_only")

	violations, ok := res.Details["allow_only_violations"].([]string)
	require.True(t, ok, "details.allow_only_violations should be a []string")
	require.Equal(t, []string{"curl"}, violations)
}

func TestToolConstraintGrader_AllowOnly_ExactMatchNotRegex(t *testing.T) {
	// Regression test for issue #586: the .agent.md contract says tool
	// names are exact, not regexes. `bash` in the allow-list must NOT
	// match `bashful` or `bash-experimental`.
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "bash"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bashful"},
			ToolCalls: []models.ToolCall{{Name: "bashful"}},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed, "'bash' allow-entry must not regex-match 'bashful'")
	require.Contains(t, res.Feedback, "bashful")
}

func TestToolConstraintGrader_AllowOnly_CaseInsensitive(t *testing.T) {
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "Bash"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{{Name: "bash"}},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed, "allow_only tool-name match must be case-insensitive")
}

func TestToolConstraintGrader_AllowOnly_DenyAllWithEmptyList(t *testing.T) {
	// AllowOnly = &[]{} is the injected representation of `tools: []` in a
	// .agent.md frontmatter — "explicitly no tools are permitted". Any
	// observed tool call must fail.
	empty := []models.ToolSpecParameters{}
	g, err := NewToolConstraintGrader("deny-all", models.ToolConstraintGraderParameters{
		AllowOnly: &empty,
	})
	require.NoError(t, err)

	t.Run("call is a violation", func(t *testing.T) {
		res, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"bash"},
				ToolCalls: []models.ToolCall{{Name: "bash"}},
			},
		})
		require.NoError(t, err)
		require.False(t, res.Passed, "with an empty allow-list, any tool call is a violation")
	})

	t.Run("no calls still passes", func(t *testing.T) {
		res, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: nil,
				ToolCalls: nil,
			},
		})
		require.NoError(t, err)
		require.True(t, res.Passed, "empty allow-list with zero calls is vacuously satisfied")
	})
}

func TestToolConstraintGrader_AllowOnly_CombinedWithExpectAndReject(t *testing.T) {
	// AllowOnly must compose with expect_tools/reject_tools. A hand-written
	// eval can use all three simultaneously.
	allow := []models.ToolSpecParameters{{Tool: "search"}, {Tool: "edit"}}
	g, err := NewToolConstraintGrader("mixed", models.ToolConstraintGraderParameters{
		ExpectTools: []models.ToolSpecParameters{{Tool: "search"}},
		RejectTools: []models.ToolSpecParameters{{Tool: "delete"}},
		AllowOnly:   &allow,
	})
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"search", "curl", "delete"},
			ToolCalls: []models.ToolCall{
				{Name: "search"}, // satisfies expect + allow
				{Name: "curl"},   // violates allow
				{Name: "delete"}, // violates reject AND allow
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	// Both undeclared tools should surface in the violation list, deduped
	// and in observation order.
	violations, ok := res.Details["allow_only_violations"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"curl", "delete"}, violations)
}

func TestToolConstraintGrader_AllowOnly_ScoreCountsEveryViolatingCall(t *testing.T) {
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "bash"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "curl"},
				{Name: "curl"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.InDelta(t, 1.0/3.0, res.Score, 0.0001)
	require.Contains(t, res.Feedback, "2 calls")
}

func TestToolConstraintGrader_AllowOnly_CommandPatternQualifier(t *testing.T) {
	// A hand-written allow_only entry can restrict which invocations of an
	// allowed tool are actually permitted, via the same qualifier fields
	// used by expect/reject.
	g, err := NewToolConstraintGrader("scoped",
		allowOnlyParams(models.ToolSpecParameters{
			Tool:           "bash",
			CommandPattern: "^ls",
		}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolsUsed: []string{"bash"},
			ToolCalls: []models.ToolCall{
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "ls -la"}},
				{Name: "bash", Arguments: models.ToolCallArgs{Command: "rm -rf /"}},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed, "bash rm-rf must fail because it doesn't match the command_pattern qualifier")
	require.Contains(t, res.Feedback, "bash")
}

func TestToolConstraintGrader_AllowOnly_ValidatesInvalidQualifier(t *testing.T) {
	// The Tool field itself is not compiled as a regex for allow-only, but
	// CommandPattern still is — an invalid regex there must be rejected at
	// construction time.
	_, err := NewToolConstraintGrader("bad",
		allowOnlyParams(models.ToolSpecParameters{
			Tool:           "bash",
			CommandPattern: "([",
		}))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "command_pattern"),
		"expected error to mention command_pattern; got %q", err.Error())
}

func TestToolConstraintGrader_AllowOnly_UnnamedCallReportedAsUnnamed(t *testing.T) {
	// Defensive: an observed call without a Name shouldn't crash and should
	// be reported so a user can trace it.
	g, err := NewToolConstraintGrader("allow",
		allowOnlyParams(models.ToolSpecParameters{Tool: "bash"}))
	require.NoError(t, err)

	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{{Name: ""}},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	violations, ok := res.Details["allow_only_violations"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"<unnamed>"}, violations)
}
