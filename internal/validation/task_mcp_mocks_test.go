package validation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const taskWithMCPMocks = `id: publish
name: Publish
inputs:
  prompt: Publish it.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_regex: {choices: "Publish this version"}
            return: {answer: Publish this version, wasFreeform: false}
          - match_schema: {}
            sequence:
              - return: {answer: Not now}
`

func TestValidateTaskBytes_MCPMocks(t *testing.T) {
	require.Empty(t, ValidateTaskBytes([]byte(taskWithMCPMocks)))
	for name, task := range map[string]string{
		"empty list":       "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks: []\n",
		"missing name":     "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks: [{tools: {a: {responses: [{return: 1}]}}}]\n",
		"no tools":         "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks: [{name: host}]\n",
		"unknown field":    "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks: [{name: host, command: x, tools: {a: {responses: [{return: 1}]}}}]\n",
		"return and error": "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks: [{name: host, tools: {a: {responses: [{return: 1, sequence: [{error: x}]}]}}}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, ValidateTaskBytes([]byte(task)))
		})
	}
	require.NotEmpty(t, ValidateTaskBytes([]byte(strings.Replace(taskWithMCPMocks, "mcp_mocks:", "mcp_mock:", 1))), "misspelled field is still rejected")
}

func TestValidateEvalFile_TaskMCPMocksRequireEvalSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	eval := `schemaVersion: "1.0"
name: old
skill: s
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: m
tasks: ["task.yaml"]
metrics:
  - name: pass
    weight: 1
    threshold: 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(taskWithMCPMocksSingle), 0o600))
	_, taskErrs, err := ValidateEvalFile(filepath.Join(dir, "eval.yaml"))
	require.NoError(t, err)
	require.Contains(t, strings.Join(taskErrs["task.yaml"], "\n"), "mcp_mocks requires schemaVersion 1.1 or newer")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(strings.Replace(eval, `"1.0"`, `"1.1"`, 1)), 0o600))
	_, taskErrs, err = ValidateEvalFile(filepath.Join(dir, "eval.yaml"))
	require.NoError(t, err)
	require.Empty(t, taskErrs)
}

const taskWithMCPMocksSingle = `id: publish
name: Publish
inputs:
  prompt: Publish it.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - return: {answer: Publish this version, wasFreeform: false}
`

func TestValidateEvalFile_TaskMCPMockNamesMustBeUnique(t *testing.T) {
	eval := `schemaVersion: "1.1"
name: names
skill: s
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: m
tasks: ["task.yaml"]
metrics:
  - name: pass
    weight: 1
    threshold: 1
`
	mock := func(name string) string {
		return "  - name: \"" + name + "\"\n    tools: {ask_user: {responses: [{return: {answer: x}}]}}\n"
	}
	for _, test := range []struct {
		name, first, second, want string
	}{
		{"exact duplicate", "host", "host", `mcp_mocks[1].name "host" is duplicated`},
		{"whitespace-equivalent duplicate", "host", " host", `mcp_mocks[1].name "host" is duplicated`},
		{"distinct names", "host", "docs", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			task := "id: t\nname: T\ninputs: {prompt: hi}\nmcp_mocks:\n" + mock(test.first) + mock(test.second)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(eval), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0o600))
			_, taskErrs, err := ValidateEvalFile(filepath.Join(dir, "eval.yaml"))
			require.NoError(t, err)
			if test.want == "" {
				require.Empty(t, taskErrs)
				return
			}
			require.Contains(t, strings.Join(taskErrs["task.yaml"], "\n"), test.want)
		})
	}
}
