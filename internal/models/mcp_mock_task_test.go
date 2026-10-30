package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func mcpMockAnswer(answer string) MCPMockTool {
	return MCPMockTool{Responses: []MCPMockResponse{{MatchSchema: map[string]any{}, Return: map[string]any{"answer": answer}}}}
}

func mcpMockReturnedAnswer(t *testing.T, mock MCPMockConfig) any {
	t.Helper()
	value, ok := mock.Tools["ask_user"].Responses[0].Return.(map[string]any)
	require.True(t, ok)
	return value["answer"]
}

func TestEffectiveMCPMocks(t *testing.T) {
	spec := &EvalSpec{MCPMocks: []MCPMockConfig{
		{Name: "host", Tools: map[string]MCPMockTool{"ask_user": mcpMockAnswer("Not now")}},
		{Name: "docs", Tools: map[string]MCPMockTool{"read_doc": mcpMockAnswer("docs")}},
	}}
	names := func(mocks []MCPMockConfig) []string {
		var out []string
		for _, mock := range mocks {
			out = append(out, mock.Name)
		}
		return out
	}

	require.Equal(t, spec.MCPMocks, EffectiveMCPMocks(spec, &TestCase{}), "a task without mcp_mocks inherits the eval list")
	require.Equal(t, spec.MCPMocks, EffectiveMCPMocks(spec, nil))
	require.Nil(t, EffectiveMCPMocks(nil, &TestCase{}))

	task := &TestCase{MCPMocks: []MCPMockConfig{
		{Name: " host ", Tools: map[string]MCPMockTool{"ask_user": mcpMockAnswer("Publish this version")}},
		{Name: "extra", Tools: map[string]MCPMockTool{"lookup": mcpMockAnswer("extra")}},
	}}
	effective := EffectiveMCPMocks(spec, task)
	require.Equal(t, []string{" host ", "docs", "extra"}, names(effective), "replacement keeps the eval position and new names are appended")
	require.Equal(t, "Publish this version", mcpMockReturnedAnswer(t, effective[0]))
	require.Equal(t, "Not now", mcpMockReturnedAnswer(t, spec.MCPMocks[0]), "the eval list is not modified")

	require.Equal(t, []string{"extra"}, names(EffectiveMCPMocks(&EvalSpec{}, &TestCase{MCPMocks: task.MCPMocks[1:]})))
}

func TestParseTestCase_MCPMocks(t *testing.T) {
	const task = `id: publish
name: Publish
inputs:
  prompt: Publish it.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_regex: {question: publish}
            return: {answer: Publish this version}
`
	tc, err := ParseTestCase([]byte(task), "task.yaml")
	require.NoError(t, err)
	require.Len(t, tc.MCPMocks, 1)
	require.Equal(t, "host", tc.MCPMocks[0].Name)
	require.Equal(t, map[string]string{"question": "publish"}, tc.MCPMocks[0].Tools["ask_user"].Responses[0].MatchRegex)
	require.NoError(t, tc.ValidateForExecutor("copilot-sdk"))
	require.ErrorContains(t, tc.ValidateForExecutor("mock"), "mcp_mocks requires executor copilot-sdk")

	_, err = ParseTestCase([]byte("id: empty\nname: Empty\ninputs:\n  prompt: hi\nmcp_mocks: []\n"), "task.yaml")
	require.ErrorContains(t, err, "mcp_mocks must not be empty")

	_, err = ParseTestCase([]byte(`id: dup
name: Dup
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools: {a: {responses: [{return: 1}]}}
  - name: " host"
    tools: {b: {responses: [{return: 2}]}}
`), "task.yaml")
	require.ErrorContains(t, err, `mcp_mocks[1].name "host" is duplicated`)

	_, err = ParseTestCase([]byte("id: unnamed\nname: Unnamed\ninputs:\n  prompt: hi\nmcp_mocks:\n  - tools: {a: {responses: [{return: 1}]}}\n"), "task.yaml")
	require.ErrorContains(t, err, "mcp_mocks[0].name is required")
}

func TestTaskMCPMockSequencesFollowTheEnclosingSchemaVersion(t *testing.T) {
	task := []byte(`id: sequence
name: Sequence
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            sequence:
              - return: {answer: first}
              - error: unavailable
`)
	_, err := ParseTestCase(task, "task.yaml")
	require.NoError(t, err, "a standalone task proves sequence shape only")
	require.NoError(t, ValidateTaskFaultSources(task, ScenarioSchemaVersion))
	require.Error(t, ValidateTaskFaultSources(task, "1.1"), "finite task MCP sequences need scenario 2.0, like eval sequences")

	invalidStep := []byte(`id: bad
name: Bad
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            sequence:
              - return: {answer: x}
                error: both
`)
	require.Error(t, ValidateTaskFaultSources(invalidStep, ScenarioSchemaVersion))
}

func TestParseTestCaseOffline_GuardsTaskMCPMatchSchemas(t *testing.T) {
	task := []byte(`id: remote
name: Remote
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {$ref: "https://example.invalid/schema.json"}
            return: {answer: x}
`)
	_, err := ParseTestCaseOffline(task, "task.yaml", nil)
	require.Error(t, err, "offline task parsing must not load an external matcher schema")
}

func TestTaskMCPMocksOmittedFromJSONWhenAbsent(t *testing.T) {
	data, err := json.Marshal(&TestCase{TestID: "plain"})
	require.NoError(t, err)
	require.NotContains(t, string(data), "mcp_mocks", "tasks without the field keep their previous cache identity")
}

func TestValidateMCPMocksSchemaVersion(t *testing.T) {
	require.ErrorContains(t, ValidateMCPMocksSchemaVersion(&EvalSpec{SchemaVersion: "1.0"}), "mcp_mocks requires schemaVersion 1.1 or newer")
	require.NoError(t, ValidateMCPMocksSchemaVersion(&EvalSpec{SchemaVersion: "1.1"}))
	require.NoError(t, ValidateMCPMocksSchemaVersion(&EvalSpec{SchemaVersion: ScenarioSchemaVersion, Scenario: "s"}))
}

func TestParseTestCaseOffline_DuplicateMCPMockNamesWithExternalSchema(t *testing.T) {
	task := []byte(`id: dup
name: Dup
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {$ref: "https://example.invalid/schema.json"}
            return: {answer: x}
  - name: host
    tools:
      other:
        responses: [{return: {answer: y}}]
`)
	_, err := ParseTestCaseOffline(task, "task.yaml", nil)
	require.ErrorContains(t, err, `mcp_mocks[1].name "host" is duplicated`)
}
