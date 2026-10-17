package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseEvaluationOutcome_WeightedScorePresenceRoundTrip(t *testing.T) {
	for _, field := range []string{"", "null", "0", "0.75"} {
		t.Run(field, func(t *testing.T) {
			suite, task := "", ""
			if field != "" {
				suite = `,"weighted_score":` + field
				task = `,"avg_weighted_score":` + field
			}
			artifact := `{"summary":{"total_tests":1` + suite + `},"tasks":[{"stats":{"avg_score":0.5` + task + `}}]}`
			nested := `{"schemaVersion":"1.5","baseline_outcome":` + artifact + `}`
			outcome, err := ParseEvaluationOutcome([]byte(nested), "presence.json")
			require.NoError(t, err)
			require.Equal(t, "1.5", outcome.SchemaVersion)
			baseline := outcome.BaselineOutcome
			require.NotNil(t, baseline)
			require.Equal(t, CurrentSchemaVersion, baseline.SchemaVersion)
			present := field != "" && field != "null"
			require.Equal(t, present, baseline.Digest.HasWeightedScore())
			require.Equal(t, present, baseline.TestOutcomes[0].Stats.HasAvgWeightedScore())
			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			if !present {
				require.NotContains(t, string(data), `"weighted_score":`)
				require.NotContains(t, string(data), `"avg_weighted_score":`)
			}
			reloaded, err := ParseEvaluationOutcome(data, "round-trip.json")
			require.NoError(t, err)
			require.Equal(t, baseline, reloaded.BaselineOutcome)
		})
	}
	writer := EvaluationOutcome{
		Digest:       OutcomeDigest{WeightedScore: 0},
		TestOutcomes: []TestOutcome{{Stats: &TestStats{AvgWeightedScore: 0}}},
	}
	require.True(t, writer.Digest.HasWeightedScore())
	require.True(t, writer.TestOutcomes[0].Stats.HasAvgWeightedScore())
	data, err := json.Marshal(writer)
	require.NoError(t, err)
	require.Contains(t, string(data), `"schemaVersion":"1.7"`)
	require.Contains(t, string(data), `"weighted_score":0`)
	require.Contains(t, string(data), `"avg_weighted_score":0`)
}

func TestParseEvaluationOutcome_RejectsUnsupportedNestedVersions(t *testing.T) {
	for _, version := range []string{"2.0", "3.0", "invalid", "1", "1.-1"} {
		for _, depth := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s/depth-%d", version, depth), func(t *testing.T) {
				nested := minimalOutcomeJSON(version)
				for range depth {
					nested = strings.Replace(minimalOutcomeJSON("1.5"),
						`"tasks": []`, `"tasks": [], "baseline_outcome": `+nested, 1)
				}
				outcome, err := ParseEvaluationOutcome([]byte(nested), "nested-results.json")
				require.Error(t, err)
				require.Nil(t, outcome)
				require.Contains(t, err.Error(), version)
				require.Contains(t, err.Error(), "nested-results.json"+strings.Repeat(".baseline_outcome", depth))
			})
		}
	}
}

func TestParseEvaluationOutcome_NestedHistoricalVersionsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []string
		want     []string
	}{
		{"unversioned", []string{"", "", ""}, []string{"1.4", "1.4", "1.4"}},
		{"deep-mixed", []string{"1.5", "", "1.0", "", "1.4", "1.5", "1.99"}, []string{"1.5", "1.4", "1.0", "1.4", "1.4", "1.5", "1.99"}},
		{"unversioned-outer", []string{"", "1.0", "1.5", "", "1.99"}, []string{"1.4", "1.0", "1.5", "1.4", "1.99"}},
		{"blank-nested", []string{"1.4", "   ", "1.1"}, []string{"1.4", "1.4", "1.1"}},
		{"no-baseline", []string{""}, []string{"1.4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var nested string
			for i := len(tc.versions) - 1; i >= 0; i-- {
				data := minimalOutcomeJSON(tc.versions[i])
				if nested != "" {
					data = strings.Replace(data, `"tasks": []`, `"tasks": [], "baseline_outcome": `+nested, 1)
				}
				nested = data
			}
			outcome, err := ParseEvaluationOutcome([]byte(nested), "historical-baseline.json")
			require.NoError(t, err)
			current := outcome
			for _, version := range tc.want {
				require.NotNil(t, current)
				require.Equal(t, version, current.SchemaVersion)
				current = current.BaselineOutcome
			}
			require.Nil(t, current)

			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			var restored EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &restored))
			current = &restored
			for _, version := range tc.want {
				require.NotNil(t, current)
				require.Equal(t, version, current.SchemaVersion)
				current = current.BaselineOutcome
			}
			require.Nil(t, current)
			require.NotContains(t, string(data), "pass_rate_ci")
			require.NotContains(t, string(data), "success_rate_ci")
			require.NotContains(t, string(data), "pass_hat_k")
		})
	}
}

func TestEvaluationOutcomeMarshal_FreshNestedWriterDefaultsRemainCurrent(t *testing.T) {
	fresh := EvaluationOutcome{
		BaselineOutcome: &EvaluationOutcome{
			BaselineOutcome: &EvaluationOutcome{SchemaVersion: "1.0"},
		},
	}
	data, err := json.Marshal(fresh)
	require.NoError(t, err)
	var restored EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, "1.7", restored.SchemaVersion)
	require.NotNil(t, restored.BaselineOutcome)
	require.Equal(t, "1.7", restored.BaselineOutcome.SchemaVersion)
	require.NotNil(t, restored.BaselineOutcome.BaselineOutcome)
	require.Equal(t, "1.0", restored.BaselineOutcome.BaselineOutcome.SchemaVersion)
}

func TestStatisticalSummary_BootstrapPresenceRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		present      bool
	}{
		{"absent", `{}`, false},
		{"recorded-zero", `{"bootstrap_ci":{"lower":0,"upper":0,"mean":0,"confidence_level":0.95,"num_bootstraps":10000}}`, true},
		{"recorded-nonzero", `{"bootstrap_ci":{"lower":0.2,"upper":0.9,"mean":0.6,"confidence_level":0.95,"num_bootstraps":10000}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := withSummaryStatistics("1.4", tc.fields)
			outcome, err := ParseEvaluationOutcome([]byte(input), "historical-statistics.json")
			require.NoError(t, err)
			require.NotNil(t, outcome.Digest.Statistics)
			require.Equal(t, tc.present, outcome.Digest.Statistics.BootstrapCI != nil)
			data, err := json.Marshal(outcome.Digest.Statistics)
			require.NoError(t, err)
			var before, after map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(tc.fields), &before))
			require.NoError(t, json.Unmarshal(data, &after))
			require.Equal(t, tc.present, after["bootstrap_ci"] != nil)
			if tc.present {
				require.JSONEq(t, string(before["bootstrap_ci"]), string(after["bootstrap_ci"]))
			}
		})
	}
}
