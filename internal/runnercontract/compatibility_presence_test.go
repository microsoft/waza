package runnercontract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunnerLocalAdapterPresenceAndNoExecutionOnRejection(t *testing.T) {
	eval, err := os.ReadFile("testdata/local/eval-1.0.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("testdata/local/task.yaml")
	require.NoError(t, err)
	selection := Selection{Shard: "local", Task: "selected", Trial: 1, Attempt: 1}
	for _, tc := range []struct {
		name, addition string
		accepted       bool
	}{
		{"omitted", "", true},
		{"null-enabled", "enabled: null\n", true},
		{"enabled", "enabled: true\n", true},
		{"disabled", "enabled: false\n", false},
		{"null-timeout", "timeout_seconds: null\n", true},
		{"zero-timeout", "timeout_seconds: 0\n", false},
		{"empty-graders", "graders: []\n", true},
		{"null-graders", "graders: null\n", true},
		{"empty-command-mocks", "command_mocks: []\n", false},
		{"null-command-mocks", "command_mocks: null\n", true},
		{"unknown-field", "unknown_contract_field: true\n", false},
		{"empty-grader", "graders:\n  - name: empty\n    type: text\n    config: {}\n", false},
		{"bad-regex", "graders:\n  - name: invalid\n    type: text\n    config:\n      regex_match: ['[']\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := string(task) + tc.addition
			request, err := AdaptLocal(eval, []byte(source), "frozen", selection, 30000)
			if tc.accepted {
				require.NoError(t, err)
				require.Equal(t, source, request.Input.Task)
				result := NewService().Run(t.Context(), wire(t, request))
				require.Equal(t, Success, result.Outcome, "%+v", result.Diagnostics)
			} else {
				require.Error(t, err)
				require.Nil(t, request)
				service := NewService(WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
					t.Fatal("invalid mapping constructed an engine")
					return nil, nil
				}))
				original := fixture(t)
				original.Input.Eval, original.Input.Task = string(eval), source
				original.Selection = selection
				original.Input.Shard = selection.Shard
				result := service.Run(t.Context(), wire(t, original))
				require.Equal(t, InvalidRequest, result.Outcome)
				require.Nil(t, result.Observation)
			}
		})
	}
}

func TestRunnerResultWirePresenceAndClassification(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	data := wireResult(t, &result)
	for _, bad := range []string{
		strings.Replace(data, `"score":1`, `"score":null`, 1),
		strings.Replace(data, `"score":1,`, "", 1),
		strings.Replace(data, `"passed":true`, `"passed":null`, 1),
		strings.Replace(data, `"duration_ms":0`, `"duration_ms":null`, 1),
		strings.Replace(strings.Replace(data, `"outcome":"success"`, `"outcome":"evaluation_failure"`, 1),
			`"exit_code":0`, `"exit_code":1`, 1),
	} {
		require.NotEqual(t, data, bad, "mutation must exercise the wire")
		_, err := ParseResult([]byte(bad))
		require.Error(t, err)
	}
}

func TestRunnerReloadPreservesExactNumbersAtEveryDynamicBoundary(t *testing.T) {
	for _, token := range []string{"9007199254740993", "-9007199254740993", "0.1234567890123456789", "1e1000"} {
		t.Run(token, func(t *testing.T) {
			result := NewService().Run(t.Context(), wire(t, fixture(t)))
			check := result.Observation.Validations["_output_contains"]
			check.Details = map[string]any{"count": json.Number(token)}
			result.Observation.Validations["_output_contains"] = check
			result.Observation.ToolEvents = []models.ToolEvent{{Args: map[string]any{"count": json.Number(token)}}}
			data := wireResult(t, &result)
			// A custom nested transcript decoder must not undo UseNumber.
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(data), &fields))
			var observation map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(fields["observation"], &observation))
			observation["transcript"] = json.RawMessage(`[{"type":"tool.execution_start","tool_call_id":"call","tool_name":"offline","arguments":{"count":` + token + `}}]`)
			observationBytes, err := json.Marshal(observation)
			require.NoError(t, err)
			fields["observation"] = observationBytes
			dataBytes, err := json.Marshal(fields)
			require.NoError(t, err)
			parsed, err := ParseResult(dataBytes)
			require.NoError(t, err)
			reloaded := wireResult(t, parsed)
			require.Contains(t, reloaded, `"count":`+token)
			require.Equal(t, 3, strings.Count(reloaded, `"count":`+token))
		})
	}
}
