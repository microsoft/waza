package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRegradeOutcomePreservesRecordedSkillAndProvenance(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.4", "1.5", "1.6"} {
		for _, tc := range []struct {
			name, field, value string
			present            bool
		}{
			{name: "absent"},
			{name: "null", field: `,"skill":null`},
			{name: "blank", field: `,"skill":""`, present: true},
			{name: "named", field: `,"skill":"recorded-skill"`, value: "recorded-skill", present: true},
			{name: "authored-unavailable", field: `,"skill":"Unavailable"`, value: "Unavailable", present: true},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				header := ""
				if version != "" {
					header = `,"schemaVersion":"` + version + `"`
				}
				raw := []byte(`{"eval_id":"recorded","eval_name":"original-eval","timestamp":"2026-10-14T00:00:00Z","config":{"model_id":"original-model","runs_per_test":3},"summary":{"duration_ms":42},"metadata":{"marker":"original"},"is_baseline":true,"baseline_outcome":{"schemaVersion":"1.0","skill":""},"evaluation_usage":{"sessions":[{"session_id":"original-session"}]}` + header + tc.field + `}`)
				original, err := models.ParseEvaluationOutcome(raw, "regrade-source.json")
				require.NoError(t, err)
				before, err := json.Marshal(original)
				require.NoError(t, err)
				graded := []models.TestOutcome{{TestID: "task", Status: models.StatusPassed, Runs: trialRuns(3, 3)}}
				result := RegradeOutcome(original, graded, "", "")
				require.Equal(t, tc.value, result.SkillTested)
				require.Equal(t, tc.present, result.HasSkillTested())
				require.Equal(t, "1.6", result.SchemaVersion)
				require.Equal(t, original.RunID, result.RunID)
				require.Equal(t, original.BenchName, result.BenchName)
				require.Equal(t, original.Timestamp, result.Timestamp)
				require.Equal(t, original.Metadata, result.Metadata)
				require.Equal(t, original.EvaluationUsage, result.EvaluationUsage)
				require.Equal(t, original.BaselineOutcome, result.BaselineOutcome)
				require.Equal(t, original.IsBaseline, result.IsBaseline)
				require.NotNil(t, result.TestOutcomes[0].Stats)
				require.NotNil(t, result.Digest.Statistics)
				require.Equal(t, int64(42), result.Digest.DurationMs)
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(encoded, &fields))
				require.Equal(t, tc.present, fields["skill"] != nil)
				restored, err := models.ParseEvaluationOutcome(encoded, "regraded.json")
				require.NoError(t, err)
				require.Equal(t, tc.present, restored.HasSkillTested())
				require.Equal(t, "1.0", restored.BaselineOutcome.SchemaVersion)
				require.True(t, restored.BaselineOutcome.HasSkillTested())
				after, err := json.Marshal(original)
				require.NoError(t, err)
				require.Equal(t, before, after, "regrading must not alter the source artifact")
			})
		}
	}
	fresh := RegradeOutcome(&models.EvaluationOutcome{}, nil, "", "")
	require.True(t, fresh.HasSkillTested())
	data, err := json.Marshal(fresh)
	require.NoError(t, err)
	require.Contains(t, string(data), `"skill":""`)
}
