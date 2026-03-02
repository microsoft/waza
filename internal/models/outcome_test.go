package models

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeStdDev(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		want   float64
	}{
		{name: "empty", values: []float64{}, want: 0.0},
		{name: "single value", values: []float64{0.5}, want: 0.0},
		{name: "identical values", values: []float64{0.8, 0.8, 0.8}, want: 0.0},
		{name: "known values", values: []float64{2.0, 4.0, 4.0, 4.0, 5.0, 5.0, 7.0, 9.0}, want: 2.0},
		{name: "two values", values: []float64{0.0, 1.0}, want: 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeStdDev(tt.values)
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestComputeRunScore(t *testing.T) {
	tests := []struct {
		name string
		run  RunResult
		want float64
	}{
		{name: "no validations", run: RunResult{}, want: 0.0},
		{
			name: "single validation",
			run:  RunResult{Validations: map[string]GraderResults{"check": {Score: 0.75, Passed: true}}},
			want: 0.75,
		},
		{
			name: "multiple validations",
			run: RunResult{Validations: map[string]GraderResults{
				"a": {Score: 1.0, Passed: true},
				"b": {Score: 0.5, Passed: false},
			}},
			want: 0.75,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run.ComputeRunScore()
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestComputeWeightedRunScore(t *testing.T) {
	tests := []struct {
		name string
		run  RunResult
		want float64
	}{
		{name: "no validations", run: RunResult{}, want: 0.0},
		{
			name: "single validation default weight",
			run:  RunResult{Validations: map[string]GraderResults{"check": {Score: 0.75, Weight: 1.0}}},
			want: 0.75,
		},
		{
			name: "equal weights same as unweighted",
			run: RunResult{Validations: map[string]GraderResults{
				"a": {Score: 1.0, Weight: 1.0},
				"b": {Score: 0.5, Weight: 1.0},
			}},
			want: 0.75,
		},
		{
			name: "weighted favoring higher scorer",
			run: RunResult{Validations: map[string]GraderResults{
				"a": {Score: 1.0, Weight: 3.0},
				"b": {Score: 0.0, Weight: 1.0},
			}},
			want: 0.75, // (1.0*3 + 0.0*1) / (3+1) = 0.75
		},
		{
			name: "zero weight defaults to 1.0",
			run: RunResult{Validations: map[string]GraderResults{
				"a": {Score: 1.0, Weight: 0.0},
				"b": {Score: 0.5, Weight: 0.0},
			}},
			want: 0.75, // treated as equal weight 1.0
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run.ComputeWeightedRunScore()
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestAllValidationsPassed(t *testing.T) {
	tests := []struct {
		name string
		run  RunResult
		want bool
	}{
		{name: "no validations passes", run: RunResult{}, want: true},
		{
			name: "all passed",
			run:  RunResult{Validations: map[string]GraderResults{"a": {Passed: true}, "b": {Passed: true}}},
			want: true,
		},
		{
			name: "one failed",
			run:  RunResult{Validations: map[string]GraderResults{"a": {Passed: true}, "b": {Passed: false}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run.AllValidationsPassed()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTestStatsStdDevScore(t *testing.T) {
	scores := []float64{1.0, 0.5, 0.5}
	got := ComputeStdDev(scores)

	mean := (1.0 + 0.5 + 0.5) / 3.0
	variance := ((1.0-mean)*(1.0-mean) + (0.5-mean)*(0.5-mean) + (0.5-mean)*(0.5-mean)) / 3.0
	want := math.Sqrt(variance)

	require.InDelta(t, want, got, 1e-9)
}

func TestUsageStats_IsZero(t *testing.T) {
	require.True(t, (&UsageStats{}).IsZero())
	require.False(t, (&UsageStats{InputTokens: 1}).IsZero())
	require.False(t, (&UsageStats{PremiumRequests: 1}).IsZero())
}

func TestAggregateUsageStats(t *testing.T) {
	stats := []*UsageStats{
		{
			InputTokens:     1000,
			OutputTokens:    500,
			PremiumRequests: 2,
			ModelMetrics: map[string]ModelUsage{
				"gpt-4o": {InputTokens: 1000, OutputTokens: 500, RequestCost: 2},
			},
		},
		{
			InputTokens:     800,
			OutputTokens:    300,
			CacheReadTokens: 100,
			PremiumRequests: 1,
			ModelMetrics: map[string]ModelUsage{
				"gpt-4o":          {InputTokens: 400, OutputTokens: 150, RequestCost: 0.5},
				"claude-sonnet-4": {InputTokens: 400, OutputTokens: 150, RequestCost: 0.5},
			},
		},
		nil, // should be skipped
	}

	agg := AggregateUsageStats(stats)
	require.NotNil(t, agg)
	require.Equal(t, 1800, agg.InputTokens)
	require.Equal(t, 800, agg.OutputTokens)
	require.Equal(t, 100, agg.CacheReadTokens)
	require.Equal(t, 3.0, agg.PremiumRequests)
	require.Len(t, agg.ModelMetrics, 2)
	require.Equal(t, 1400, agg.ModelMetrics["gpt-4o"].InputTokens)
}

func TestAggregateUsageStats_AllNil(t *testing.T) {
	require.Nil(t, AggregateUsageStats([]*UsageStats{nil, nil}))
}

func TestAggregateUsageStats_Empty(t *testing.T) {
	require.Nil(t, AggregateUsageStats(nil))
}
