package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
)

// The runner request/terminal schemas inline the budget definitions so every
// compile site stays single-resource; they must not drift from the source.
func TestExecutionBudgetDefinitionsAreInlinedVerbatim(t *testing.T) {
	defs := func(text string) map[string]any {
		var doc struct {
			Defs map[string]any `json:"$defs"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &doc))
		return doc.Defs
	}
	source := defs(schemas.ExecutionBudgetSchemaJSON)
	require.NotNil(t, source["request"])
	require.NotNil(t, source["report"])
	require.Equal(t, source["request"], defs(schemas.RunnerRequestSchemaJSON)["budgets"])
	require.Equal(t, source["report"], defs(schemas.RunnerTerminalSchemaJSON)["budget"])
}
