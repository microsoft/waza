package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScenarioSchema(t *testing.T) {
	for _, tt := range []struct {
		fields string
		valid  bool
	}{
		{"skill: reader\n", true},
		{"scenario: inventory\nschemaVersion: \"2.0\"\n", true},
		{"scenario: inventory\nskill: reader\nschemaVersion: \"2.0\"\n", true},
		{"scenario: inventory\n", false},
		{"scenario: inventory\nschemaVersion: \"1.5\"\n", false},
		{"scenario: inventory\nschemaVersion: \"2.1\"\n", false},
		{"scenario: \" \"\nschemaVersion: \"2.0\"\n", false},
		{"schemaVersion: \"2.0\"\nskill: reader\n", false},
		{"", false},
		{"scenario: inventory\nschemaVersion: \"2.0\"\ncontext: {agent: reader}\n", false},
	} {
		t.Run(fmt.Sprintf("%q", tt.fields), func(t *testing.T) {
			data := tt.fields + `name: suite
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: mock
metrics:
  - name: completion
    weight: 1
    threshold: 1
tasks: ["tasks/*.yaml"]
`
			if tt.valid {
				require.Empty(t, ValidateEvalBytes([]byte(data)))
			} else {
				require.NotEmpty(t, ValidateEvalBytes([]byte(data)))
			}
		})
	}
}
