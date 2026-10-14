package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestBuildDigest_SuccessRateCIIndependentOfTrialStatistics(t *testing.T) {
	passed := models.TestOutcome{Status: models.StatusPassed, Stats: ComputeTestStats(trialRuns(3, 3))}
	failed := models.TestOutcome{Status: models.StatusFailed, Stats: ComputeTestStats(trialRuns(1, 3))}
	for _, tc := range []struct {
		name       string
		outcomes   []models.TestOutcome
		total      int
		passed     int
		lower      float64
		upper      float64
		bootstrap  bool
		curveCount int
	}{
		{
			name:     "partial-stats-before-task-failure",
			outcomes: []models.TestOutcome{passed, {Status: models.StatusFailed}},
			total:    2, passed: 1, lower: 0.0945, upper: 0.9055,
		},
		{
			name:     "all-stats-missing-known-statuses",
			outcomes: []models.TestOutcome{{Status: models.StatusPassed}, {Status: models.StatusFailed}},
			total:    2, passed: 1, lower: 0.0945, upper: 0.9055,
		},
		{
			name:     "all-fail-before-task-execution",
			outcomes: []models.TestOutcome{{Status: models.StatusFailed}, {Status: models.StatusError}},
			total:    2, passed: 0, lower: 0, upper: 0.6576,
		},
		{
			name:     "bootstrap-available-but-third-task-has-no-trials",
			outcomes: []models.TestOutcome{passed, passed, {Status: models.StatusError}},
			total:    3, passed: 2, lower: 0.2077, upper: 0.9385, bootstrap: true,
		},
		{
			name:     "all-trial-statistics-available",
			outcomes: []models.TestOutcome{passed, failed},
			total:    2, passed: 1, lower: 0.0945, upper: 0.9055, bootstrap: true, curveCount: 3,
		},
		{
			name:     "single-task-multiple-trials",
			outcomes: []models.TestOutcome{passed},
			total:    1, passed: 1, lower: 0.2065, upper: 1, curveCount: 3,
		},
		{
			name:     "empty-stats-are-not-score-or-trial-evidence",
			outcomes: []models.TestOutcome{{Status: models.StatusPassed, Stats: &models.TestStats{}}, {Status: models.StatusError, Stats: &models.TestStats{}}},
			total:    2, passed: 1, lower: 0.0945, upper: 0.9055,
		},
		{
			name:     "skipped-and-error-outcomes-remain-in-denominator",
			outcomes: []models.TestOutcome{{Status: models.StatusPassed}, {Status: models.StatusSkipped}, {Status: models.StatusError}},
			total:    3, passed: 1, lower: 0.0615, upper: 0.7923,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := BuildDigest(tc.outcomes, 1000, 3)
			require.Equal(t, tc.total, digest.TotalTests)
			require.Equal(t, tc.passed, digest.Succeeded)
			require.NotNil(t, digest.Statistics)
			ci := digest.Statistics.SuccessRateCI
			require.NotNil(t, ci)
			require.InDelta(t, float64(tc.passed)/float64(tc.total), ci.Mean, 1e-9)
			require.InDelta(t, ci.Mean, digest.SuccessRate, 1e-9)
			require.InDelta(t, tc.lower, ci.Lower, 1e-4)
			require.InDelta(t, tc.upper, ci.Upper, 1e-4)
			require.Equal(t, 0.95, ci.ConfidenceLevel)
			require.Equal(t, tc.bootstrap, digest.Statistics.BootstrapCI != nil)
			require.Len(t, digest.Statistics.PassHatK, tc.curveCount)
			if tc.curveCount == 0 {
				require.Nil(t, digest.Statistics.PassHatK)
			}
			data, err := json.Marshal(digest.Statistics)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.Contains(t, fields, "success_rate_ci")
			require.Equal(t, tc.bootstrap, fields["bootstrap_ci"] != nil)
			require.Equal(t, tc.curveCount > 0, fields["pass_hat_k"] != nil)
		})
	}
}

func TestBuildDigest_SuccessRateCIRequiresMultipleConfiguredTrialsAndNonemptyTaskOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcomes []models.TestOutcome
		trials   int
	}{
		{"no-tasks", nil, 3},
		{"empty-tasks", []models.TestOutcome{}, 3},
		{"single-trial", []models.TestOutcome{{Status: models.StatusPassed, Stats: ComputeTestStats(trialRuns(1, 1))}}, 1},
		{"zero-trials", []models.TestOutcome{{Status: models.StatusFailed}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := BuildDigest(tc.outcomes, 0, tc.trials)
			require.Nil(t, digest.Statistics)
		})
	}
}
