package models

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluationOutcomeRecordedSkillPresence(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.4", "1.5"} {
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
					header = fmt.Sprintf(`,"schemaVersion":%q`, version)
				}
				raw := []byte(`{"eval_id":"recorded","eval_name":"not-a-skill","summary":{},"tasks":[]` + header + tc.field + `}`)
				outcome, err := ParseEvaluationOutcome(raw, "recorded-skill.json")
				require.NoError(t, err)
				wantVersion := version
				if wantVersion == "" {
					wantVersion = CurrentSchemaVersion
				}
				for range 2 {
					require.Equal(t, tc.value, outcome.SkillTested)
					require.Equal(t, tc.present, outcome.HasSkillTested())
					require.Equal(t, wantVersion, outcome.SchemaVersion)
					require.False(t, outcome.Digest.HasWeightedScore())
					encoded, err := json.Marshal(outcome)
					require.NoError(t, err)
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(encoded, &fields))
					require.Equal(t, tc.present, fields["skill"] != nil)
					if tc.present {
						want, err := json.Marshal(tc.value)
						require.NoError(t, err)
						require.JSONEq(t, string(want), string(fields["skill"]))
					}
					outcome, err = ParseEvaluationOutcome(encoded, "rewritten-skill.json")
					require.NoError(t, err)
				}
				nested, err := ParseEvaluationOutcome(
					[]byte(`{"schemaVersion":"1.5","skill":"outer","baseline_outcome":`+string(raw)+`}`),
					"nested-skill.json",
				)
				require.NoError(t, err)
				require.True(t, nested.HasSkillTested())
				require.Equal(t, tc.present, nested.BaselineOutcome.HasSkillTested())
				encoded, err := json.Marshal(nested)
				require.NoError(t, err)
				restored, err := ParseEvaluationOutcome(encoded, "nested-round-trip.json")
				require.NoError(t, err)
				require.Equal(t, nested, restored)
			})
		}
	}
}

func TestEvaluationOutcomeFreshSkillWriterCompatibility(t *testing.T) {
	for _, value := range []string{"", "recorded-skill"} {
		outcome := EvaluationOutcome{SkillTested: value}
		require.True(t, outcome.HasSkillTested())
		data, err := json.Marshal(outcome)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &fields))
		require.NotNil(t, fields["skill"])
		var skill string
		require.NoError(t, json.Unmarshal(fields["skill"], &skill))
		require.Equal(t, value, skill)
		require.Equal(t, `"1.6"`, string(fields["schemaVersion"]))
	}
	legacy, err := ParseEvaluationOutcome([]byte(`{"eval_id":"legacy"}`), "legacy.json")
	require.NoError(t, err)
	require.False(t, legacy.HasSkillTested())
	legacy.SkillTested = "newly-recorded"
	require.True(t, legacy.HasSkillTested(), "public string assignments remain supported")
}

func TestEvaluationOutcomeShallowCopyPreservesRecordedPresence(t *testing.T) {
	for _, field := range []string{"", `,"skill":null`, `,"skill":""`, `,"skill":"recorded-skill"`} {
		t.Run(field, func(t *testing.T) {
			original, err := ParseEvaluationOutcome([]byte(`{"eval_id":"original","baseline_outcome":{"skill":""}`+field+`}`), "copy.json")
			require.NoError(t, err)
			copied := original.ShallowCopy()
			require.NotSame(t, original, copied)
			require.Equal(t, original, copied)
			require.Equal(t, original.HasSkillTested(), copied.HasSkillTested())
			copied.RunID = "copy"
			require.Equal(t, "original", original.RunID)
			encoded, err := json.Marshal(copied)
			require.NoError(t, err)
			restored, err := ParseEvaluationOutcome(encoded, "copied.json")
			require.NoError(t, err)
			require.Equal(t, copied, restored)
			require.True(t, restored.BaselineOutcome.HasSkillTested())
		})
	}
	fresh := EvaluationOutcome{}
	require.True(t, fresh.ShallowCopy().HasSkillTested(), "fresh writer blanks remain recorded")
}
