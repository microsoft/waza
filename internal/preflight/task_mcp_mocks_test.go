package preflight

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectTaskMCPMocks(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Not now}}]
`
	task := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Publish this version}}]
  - name: extra
    fixtures: missing-fixture-directory
`
	path, _ := writeSuite(t, eval, task)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "mock.mcp_replaced", Verified), "the only task replaces the eval host mock: %+v", r.Diagnostics)
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "host", Mode: "mocked", State: Verified, TaskID: "task"})
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "extra", Mode: "mocked", State: Invalid, TaskID: "task"})
	require.True(t, hasDiagnostic(r, "mock.mcp_task_override", Verified))
	require.True(t, hasDiagnostic(r, "mock.mcp", Invalid), "%+v", r.Diagnostics)
	for _, d := range r.Diagnostics {
		if d.Code == "mock.mcp" {
			require.Equal(t, "task", d.TaskID, "a task mock failure names its task")
		}
	}
}

func TestInspectTaskMCPMockInvalidMatcherIsTaskConfiguration(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
	task := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_regex: {question: "("}, return: {answer: Not now}}]
`
	path, _ := writeSuite(t, eval, task)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.configuration", Invalid), "%+v", r.Diagnostics)
}

func TestInspectTaskMCPMocksRequireSupportedExecutorAndVersion(t *testing.T) {
	task := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Not now}}]
`
	path, _ := writeSuite(t, validEval, task)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.executor", Invalid), "%+v", r.Diagnostics)

	eval := strings.ReplaceAll(strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk"), `schemaVersion: "1.4"`, `schemaVersion: "1.0"`)
	path, _ = writeSuite(t, eval, task)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.version", Invalid), "%+v", r.Diagnostics)
}

// An external matcher schema leaves the task unresolved, but its executor and
// fixture problems must still be reported as invalid.
func TestInspectBlockedTaskMCPMocksKeepIndependentChecks(t *testing.T) {
	task := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_schema: {$ref: "https://example.invalid/schema.json"}, return: {answer: x}}]
  - name: extra
    fixtures: missing-fixture-directory
`
	path, _ := writeSuite(t, validEval, task)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.configuration", Unresolved), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "task.executor", Invalid), "%+v", r.Diagnostics)
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "extra", Mode: "mocked", State: Invalid, TaskID: "task"})
	require.True(t, r.Failed(false))

	eval := strings.ReplaceAll(strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk"), `schemaVersion: "1.4"`, `schemaVersion: "1.0"`)
	path, _ = writeSuite(t, eval, task)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.version", Invalid), "%+v", r.Diagnostics)
}

func TestInspectBlockedTaskMCPMockDuplicateNamesAreInvalid(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
	task := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_schema: {$ref: "https://example.invalid/schema.json"}, return: {answer: x}}]
  - name: host
    tools:
      other:
        responses: [{return: {answer: y}}]
`
	path, _ := writeSuite(t, eval, task)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.configuration", Invalid), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "schema.external_reference", Unresolved), "%+v", r.Diagnostics)
}

func TestInspectLiveServerReplacedByEveryTaskIsNotInspected(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
	eval = strings.ReplaceAll(eval, "  model: harness", "  model: harness\n  mcp_servers:\n    host: {}")
	mocked := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: x}}]
`
	path, _ := writeSuite(t, eval, mocked)
	r := Inspect(path, Options{})
	require.False(t, hasDiagnostic(r, "dependency.mcp_configuration", Invalid), "%+v", r.Diagnostics)
	for _, dep := range r.Dependencies {
		require.False(t, dep.Name == "host" && dep.Mode == "live", "host never runs live: %+v", dep)
	}
	require.True(t, hasDiagnostic(r, "mock.mcp_override", Verified))

	path, _ = writeSuite(t, eval, validTask)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "dependency.mcp_configuration", Invalid), "a task that uses host live still reports its configuration")
}

func TestInspectEvalMockReplacedByEveryTaskSkipsFixtures(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`
	replacing := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Publish this version}}]
`
	path, _ := writeSuite(t, eval, replacing)
	r := Inspect(path, Options{})
	require.False(t, r.Failed(false), "%+v", r.Diagnostics)
	require.False(t, hasDiagnostic(r, "mock.mcp", Invalid), "the replaced eval mock's fixtures are not read: %+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.mcp_replaced", Verified), "%+v", r.Diagnostics)
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "host", Mode: "mocked", State: Verified, TaskID: "task"})
	for _, dep := range r.Dependencies {
		require.False(t, dep.Name == "host" && dep.TaskID == "", "an eval mock that never starts is not a dependency: %+v", dep)
	}

	path, _ = writeSuite(t, eval, validTask)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "mock.mcp", Invalid), "a task that inherits host still needs its fixtures: %+v", r.Diagnostics)
	require.False(t, hasDiagnostic(r, "mock.mcp_replaced", Verified))

	invalidInline := strings.Replace(eval, "    fixtures: missing-host-fixtures\n", "    fixtures: missing-host-fixtures\n    tools:\n      ask_user:\n        responses: [{match_regex: {question: \"(\"}, return: {answer: x}}]\n", 1)
	path, _ = writeSuite(t, invalidInline, replacing)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "eval.configuration", Invalid), "replaced eval mock declarations are still validated: %+v", r.Diagnostics)
}

func TestInspectReplacedEvalMockValidatesInputSchemas(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
    tools:
      ask_user:
        input_schema: {type: 42}
        responses: [{return: {answer: x}}]
`
	replacing := validTask + `
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Publish this version}}]
`
	path, _ := writeSuite(t, eval, replacing)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "mock.mcp_replaced", Verified), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "mock.input_schema", Invalid), "a replaced mock's inline input schema is still validated: %+v", r.Diagnostics)
	require.False(t, hasDiagnostic(r, "mock.mcp", Invalid), "its fixtures are still not read: %+v", r.Diagnostics)
}
