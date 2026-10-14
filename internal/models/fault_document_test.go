package models

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMockSourcesDetachAnchorsAndPreserveFloatTags(t *testing.T) {
	source := []byte(`shared: &step
  exit_code: !!float 1
  stderr: ""
matcher: &matcher
  args: []
  sequence: [*step]
command_mocks:
  - name: probe
    responses:
      - <<: *matcher
        sequence:
          - <<: *step
            stdout: ""
`)
	responses, err := MockResponseSources(source)
	require.NoError(t, err)
	require.Len(t, responses, 1)
	require.Equal(t, "probe", responses[0].Command)
	require.Equal(t, 0, responses[0].Index)
	require.True(t, responses[0].Finite)
	require.NotContains(t, string(responses[0].Data), "*step")
	require.NotContains(t, string(responses[0].Data), "*matcher")
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal(responses[0].Data, &node))
	sequence, err := faultDocumentValue(node.Content[0], "sequence")
	require.NoError(t, err)
	exit, err := faultDocumentValue(sequence.Content[0], "exit_code")
	require.NoError(t, err)
	require.Equal(t, "!!float", exit.Tag)
	require.Equal(t, "1", exit.Value)
}

func TestMockSourcesSelectDeclaredLocationsOnly(t *testing.T) {
	source := []byte(`mcp_mocks:
  - name: dependency
    tools:
      inspect:
        responses:
          - return:
              sequence: []
              responses: [{sequence: []}]
          - match: {operation: retry}
            sequence: [{error: denied}]
`)
	responses, err := MockResponseSources(source)
	require.NoError(t, err)
	require.Len(t, responses, 2)
	require.Equal(t, "dependency", responses[0].Server)
	require.Equal(t, "inspect", responses[0].Tool)
	require.False(t, responses[0].Finite)
	require.True(t, responses[1].Finite)
	require.Equal(t, 1, responses[1].Index)
}

func TestMockSourcesMergePrecedenceAndDuplicateRetention(t *testing.T) {
	source := []byte(`first: &first {args: [], stdout: first}
second: &second {stdout: second, exit_code: 0}
command_mocks:
  - name: probe
    responses:
      - <<: [*first, *second]
        stdout: explicit
      - args: []
        sequence:
          - stdout: one
            stdout: two
`)
	responses, err := MockResponseSources(source)
	require.NoError(t, err)
	require.Len(t, responses, 2)
	require.NotContains(t, string(responses[0].Data), "first")
	require.NotContains(t, string(responses[0].Data), "second")
	require.Contains(t, string(responses[0].Data), "explicit")
	require.Contains(t, string(responses[1].Data), "stdout: one")
	require.Contains(t, string(responses[1].Data), "stdout: two")
	mergedDuplicate := []byte(`step: &step {stdout: one, stdout: two}
command_mocks:
  - name: probe
    responses: [{args: [], sequence: [{<<: *step}]}]
`)
	responses, err = MockResponseSources(mergedDuplicate)
	require.NoError(t, err)
	require.Contains(t, string(responses[0].Data), "stdout: one")
	require.Contains(t, string(responses[0].Data), "stdout: two")
}

func TestMockSourcesRejectInvalidGraphs(t *testing.T) {
	for _, source := range []string{
		"command_mocks: &cycle [{name: probe, responses: *cycle}]",
		"command_mocks: []\n---\ncommand_mocks: []",
		"command_mocks: []\ncommand_mocks: []",
		"command_mocks: {name: probe}",
		"mcp_mocks: [{name: probe, tools: []}]",
		"command_mocks: [{name: probe, responses: {}}]",
		"command_mocks: [{name: probe, responses: [{<<: 5}]}]",
		"command_mocks: [{name: probe, responses: [{<<: [5]}]}]",
	} {
		t.Run(source, func(t *testing.T) {
			responses, err := MockResponseSources([]byte(source))
			require.Error(t, err)
			require.Nil(t, responses)
		})
	}
}

func TestMockSourcesResolveAliasedIdentity(t *testing.T) {
	source := []byte(`command_name: &command probe
server_name: &server dependency
command_mocks:
  - name: *command
    responses: [{args: [], stdout: ready}]
mcp_mocks:
  - name: *server
    tools:
      inspect:
        responses: [{return: ready}]
`)
	responses, err := MockResponseSources(source)
	require.NoError(t, err)
	require.Len(t, responses, 2)
	var decoded struct {
		CommandMocks []CommandMockConfig `yaml:"command_mocks"`
		MCPMocks     []MCPMockConfig     `yaml:"mcp_mocks"`
	}
	require.NoError(t, yaml.Unmarshal(source, &decoded))
	require.Equal(t, decoded.CommandMocks[0].Name, responses[0].Command)
	require.Equal(t, decoded.MCPMocks[0].Name, responses[1].Server)
}
