package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMCPMockStepMarshalJSONPropagatesUnsupportedReturn(t *testing.T) {
	_, err := json.Marshal(MCPMockStep{Return: make(chan int)})
	var unsupported *json.UnsupportedTypeError
	require.ErrorAs(t, err, &unsupported)
}

func TestFaultSequenceSerializationPreservesOutputs(t *testing.T) {
	for _, format := range []struct {
		name    string
		marshal func(any) ([]byte, error)
		source  FaultSourceFormat
	}{
		{"JSON", json.Marshal, FaultSourceJSON},
		{"YAML", yaml.Marshal, FaultSourceYAML},
	} {
		t.Run(format.name, func(t *testing.T) {
			t.Run("silent command success", func(t *testing.T) {
				data, err := format.marshal(CommandMockResponse{Args: []string{"read"}, Sequence: []CommandMockStep{{ExitCode: 0}}})
				require.NoError(t, err)
				source, _, err := DecodeFaultSource(data, format.source)
				require.NoError(t, err)
				require.NoError(t, ValidateCommandFaultResponse(data, format.source, ScenarioSchemaVersion))
				var steps []map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(source["sequence"], &steps))
				require.Len(t, steps, 1)
				require.JSONEq(t, `0`, string(steps[0]["exit_code"]))
			})
			for _, test := range []struct {
				name       string
				step       MCPMockStep
				wantReturn bool
				valid      bool
			}{
				{"null success", MCPMockStep{Return: nil}, true, true},
				{"error only", MCPMockStep{Error: "temporary"}, false, true},
				{"non-null success", MCPMockStep{Return: "ready", DelayMS: 1}, true, true},
				{"invalid dual output", MCPMockStep{Return: "ready", Error: "temporary"}, true, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					data, err := format.marshal(MCPMockResponse{Sequence: []MCPMockStep{test.step}})
					require.NoError(t, err)
					source, _, err := DecodeFaultSource(data, format.source)
					require.NoError(t, err)
					err = ValidateMCPFaultResponse(data, format.source, ScenarioSchemaVersion)
					if test.valid {
						require.NoError(t, err)
					} else {
						require.Error(t, err, "serialization must not repair invalid dual outputs")
					}

					var steps []map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(source["sequence"], &steps))
					require.Len(t, steps, 1)
					value, present := steps[0]["return"]
					require.Equal(t, test.wantReturn, present)
					if test.wantReturn && test.step.Return == nil {
						require.JSONEq(t, "null", string(value))
					}
					_, hasError := steps[0]["error"]
					require.Equal(t, test.step.Error != "", hasError)
				})
			}
		})
	}
}
