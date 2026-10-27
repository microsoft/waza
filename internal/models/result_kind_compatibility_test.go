package models

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestResultsKindFencePreservesUnknownSameMajorFields(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	require.NoError(t, err)
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &original))
	for _, kind := range []string{`42`, `true`, `null`, `[]`, `{}`, `"unknown"`, `"WAZA.PREFLIGHT"`, `"waza.preflight"`, `"waza.runner-result"`} {
		t.Run(kind, func(t *testing.T) {
			fields := make(map[string]json.RawMessage, len(original)+1)
			for key, value := range original {
				fields[key] = value
			}
			fields["kind"] = json.RawMessage(kind)
			bytes, err := json.Marshal(fields)
			require.NoError(t, err)
			outcome, parseErr := ParseEvaluationOutcome(bytes, "results.json")
			_, accepted, probeErr := ProbeEvaluationOutcomeSchemaVersion(bytes)
			require.NoError(t, probeErr)
			if kind == `"waza.preflight"` || kind == `"waza.runner-result"` {
				require.ErrorContains(t, parseErr, "not evaluation results")
				require.Nil(t, outcome)
				require.False(t, accepted)
			} else {
				require.NoError(t, parseErr)
				require.True(t, accepted)
				require.Equal(t, "compatibility-run", outcome.RunID)
				require.Equal(t, "1.0", outcome.SchemaVersion)
			}
		})
	}
}
