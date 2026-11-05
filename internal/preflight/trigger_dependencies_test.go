package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const preflightTriggers = "skill: example\nshould_trigger_prompts:\n  - prompt: inspect\n"
const replacingTriggerTask = validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: task}}]
`

func TestInspectTriggerDependenciesPreventTaskOnlyPruning(t *testing.T) {
	for _, mode := range []string{"absent", "valid", "malformed", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
			eval = strings.Replace(eval, "  model: harness", "  mcp_servers:\n    host: {type: stdio, command: ''}\n  model: harness", 1)
			path, dir := writeSuite(t, eval, replacingTriggerTask)
			triggerPath := filepath.Join(dir, "trigger_tests.yaml")
			switch mode {
			case "valid":
				require.NoError(t, os.WriteFile(triggerPath, []byte(preflightTriggers), 0o600))
			case "malformed":
				require.NoError(t, os.WriteFile(triggerPath, []byte("skill: ["), 0o600))
			case "unreadable":
				require.NoError(t, os.Mkdir(triggerPath, 0o700))
			}
			r := Inspect(path, Options{})
			require.Equal(t, mode != "absent", hasDiagnostic(r, "dependency.mcp_configuration", Invalid), "%+v", r.Diagnostics)
			require.Equal(t, mode == "malformed" || mode == "unreadable", hasDiagnostic(r, "trigger.configuration", Invalid), "%+v", r.Diagnostics)
		})
	}
}

func TestInspectTriggerErrorKeepsIndependentMockDiagnostics(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
mcp_mocks:
  - name: host
    fixtures: missing-fixtures
    tools:
      external:
        input_schema: {$ref: 'https://never-read.invalid/schema'}
        responses: [{return: {answer: eval}}]
      invalid:
        input_schema: {type: 42}
        responses: [{return: {answer: eval}}]
`
	path, dir := writeSuite(t, eval, replacingTriggerTask)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), []byte("skill: ["), 0o600))
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "trigger.configuration", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.mcp", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.input_schema", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.input_schema", Unresolved), "%+v", r.Diagnostics)
	require.False(t, r.Complete)
	require.True(t, r.Failed(false))
}

func TestInspectMockExecutorSynthesizesTriggersWithoutEvalMCP(t *testing.T) {
	eval := validEval + `
mcp_mocks:
  - name: host
    fixtures: missing-fixtures
`
	path, dir := writeSuite(t, eval, replacingTriggerTask)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), []byte(preflightTriggers), 0o600))
	r := Inspect(path, Options{})
	require.False(t, hasDiagnostic(r, "mock.mcp", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.mcp_replaced", Verified), "%+v", r.Diagnostics)
}

func TestInspectTriggerAndEarlyMockDeclarationErrorsBothReported(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_regex: {question: "("}, return: {answer: eval}}]
`
	path, dir := writeSuite(t, eval, replacingTriggerTask)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), []byte("skill: ["), 0o600))
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "trigger.configuration", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "eval.configuration", Invalid), "%+v", r.Diagnostics)
	require.True(t, r.Failed(false))
}
