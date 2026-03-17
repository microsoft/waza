package reporting

import (
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestInterpretScore(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  string
	}{
		{"excellent high", 0.95, "Excellent (>90%)"},
		{"excellent boundary", 0.91, "Excellent (>90%)"},
		{"good high", 0.90, "Good (70-90%)"},
		{"good mid", 0.80, "Good (70-90%)"},
		{"good low", 0.70, "Good (70-90%)"},
		{"needs work high", 0.69, "Needs Work (50-70%)"},
		{"needs work mid", 0.60, "Needs Work (50-70%)"},
		{"needs work low", 0.50, "Needs Work (50-70%)"},
		{"poor high", 0.49, "Poor (<50%)"},
		{"poor zero", 0.0, "Poor (<50%)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InterpretScore(tt.score)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInterpretPassRate(t *testing.T) {
	tests := []struct {
		name string
		rate float64
		want string
	}{
		{"all passed", 1.0, "All tests passed (100%)"},
		{"most passed", 0.85, "Most tests passed (85%)"},
		{"about half", 0.60, "About half the tests passed (60%)"},
		{"few passed", 0.30, "Few tests passed (30%)"},
		{"none passed", 0.0, "Few tests passed (0%)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InterpretPassRate(tt.rate)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInterpretFlaky(t *testing.T) {
	tests := []struct {
		name     string
		flaky    bool
		passRate float64
		contains string
	}{
		{"not flaky", false, 1.0, "consistent"},
		{"flaky", true, 0.6, "flaky"},
		{"flaky low rate", true, 0.3, "30% pass rate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InterpretFlaky(tt.flaky, tt.passRate)
			assert.Contains(t, got, tt.contains)
		})
	}
}

func TestFormatSummaryReport(t *testing.T) {
	outcome := &models.EvalOutcome{
		Digest: models.EvalDigest{
			TotalTests:     3,
			Succeeded:      2,
			Failed:         1,
			Errors:         0,
			SuccessRate:    0.67,
			AggregateScore: 0.75,
			DurationMs:     1500,
		},
		TaskOutcomes: []models.TaskOutcome{
			{
				DisplayName: "Task A",
				Status:      models.StatusPassed,
				Stats: &models.TaskStats{
					AvgScore: 0.95,
					PassRate: 1.0,
					Flaky:    false,
				},
			},
			{
				DisplayName: "Task B",
				Status:      models.StatusFailed,
				Stats: &models.TaskStats{
					AvgScore: 0.40,
					PassRate: 0.5,
					Flaky:    true,
				},
			},
		},
	}

	report := FormatSummaryReport(outcome)

	assert.Contains(t, report, "=== Interpretation ===")
	assert.Contains(t, report, "Good (70-90%)")
	assert.Contains(t, report, "2 passed, 1 failed, 0 errors out of 3 total")
	assert.Contains(t, report, "Task A")
	assert.Contains(t, report, "Task B")
	assert.Contains(t, report, "Excellent (>90%)")
	assert.Contains(t, report, "flaky")
	assert.Contains(t, report, "consistent")
}

func TestFormatSummaryReport_Empty(t *testing.T) {
	outcome := &models.EvalOutcome{
		Digest: models.EvalDigest{},
	}
	report := FormatSummaryReport(outcome)
	assert.True(t, strings.Contains(report, "Interpretation"))
}
