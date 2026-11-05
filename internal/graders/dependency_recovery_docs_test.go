package graders

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

// The guide publishes this grader as copyable YAML, so execute it through the
// production loader and JavaScript wrapper against recorded tool-event shapes.
func TestDependencyRecoveryPublishedSequenceGrader(t *testing.T) {
	skipIfNoJavascript(t)
	data, err := os.ReadFile(testutil.RepoFile(t, "site", "src", "content", "docs", "guides", "dependency-recovery.mdx"))
	require.NoError(t, err)
	var suite string
	for _, block := range regexp.MustCompile("(?s)```yaml\r?\n(.*?)```").FindAllStringSubmatch(string(data), -1) {
		if strings.HasPrefix(block[1], "name: multi-turn-mcp-sequence") {
			suite = strings.ReplaceAll(block[1], "\r\n", "\n")
		}
	}
	require.NotEmpty(t, suite, "the guide must publish the multi-turn MCP eval")
	path := filepath.Join(t.TempDir(), "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(suite), 0o600))
	spec, err := models.LoadEvalSpec(path)
	require.NoError(t, err)
	var config *models.GraderConfig
	for i := range spec.Graders {
		if spec.Graders[i].Identifier == "observed-sequence" {
			config = &spec.Graders[i]
		}
	}
	require.NotNil(t, config)
	grader, err := Create(config.Identifier, config.Parameters)
	require.NoError(t, err)

	contentResult := func(ordinal string) any {
		text := `{"ordinal":` + ordinal + `}`
		return map[string]any{
			"content":         text,
			"contents":        []any{map[string]any{"type": "text", "text": text}},
			"detailedContent": text,
		}
	}
	jsonStringResult := func(ordinal string) any { return `{"ordinal":` + ordinal + `}` }
	events := func(result func(string) any, first, second string, extra ...models.ToolEvent) []models.ToolEvent {
		return append([]models.ToolEvent{
			{ToolName: "dependency-read", Success: true, Result: result(first)},
			{ToolName: "dependency-read", Success: true, Result: result(second)},
		}, extra...)
	}
	for _, test := range []struct {
		name   string
		events []models.ToolEvent
		passed bool
	}{
		{"recorded content result", events(contentResult, "1", "2"), true},
		{"JSON string result", events(jsonStringResult, "1", "2"), true},
		{"content near miss", events(contentResult, "11", "22"), false},
		{"JSON string near miss", events(jsonStringResult, "11", "22"), false},
		{"reset sequence", events(contentResult, "1", "1"), false},
		{"string ordinal", events(func(string) any { return `{"ordinal":"1"}` }, "1", "2"), false},
		{"failed call", []models.ToolEvent{
			{ToolName: "dependency-read", Success: true, Result: contentResult("1")},
			{ToolName: "dependency-read", Success: false, Result: contentResult("2")},
		}, false},
		{"extra call", events(contentResult, "1", "2", models.ToolEvent{ToolName: "dependency-read", Success: true, Result: contentResult("2")}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := grader.Grade(t.Context(), &Context{ToolEvents: test.events})
			require.NoError(t, err)
			require.Equal(t, test.passed, result.Passed, "%+v", result)
		})
	}
}
