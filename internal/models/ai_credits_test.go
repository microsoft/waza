package models

import (
	"testing"

	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestAggregateCreditCompleteness(t *testing.T) {
	reported := &UsageStats{InputTokens: 1, AICredits: utils.Ptr(1.0), ModelMetrics: map[string]ModelUsage{
		"a": {InputTokens: 1, AICredits: utils.Ptr(1.0)},
	}}
	missing := &UsageStats{InputTokens: 2, ModelMetrics: map[string]ModelUsage{"a": {InputTokens: 2}}}
	for _, stats := range [][]*UsageStats{{reported, missing}, {missing, reported}, {reported, nil}, {nil, reported}} {
		agg := AggregateUsageStats(stats)
		require.Nil(t, agg.AICredits)
		if stats[0] != nil && stats[1] != nil {
			require.Nil(t, agg.ModelMetrics["a"].AICredits)
		}
	}
	agg := AggregateUsageStats([]*UsageStats{reported, reported})
	require.Equal(t, 2.0, *agg.AICredits)
	require.Equal(t, 2.0, *agg.ModelMetrics["a"].AICredits)
	agg = AggregateUsageStats([]*UsageStats{reported, {
		AICredits: utils.Ptr(0.0), ModelMetrics: map[string]ModelUsage{
			"b": {AICredits: utils.Ptr(0.0)},
		},
	}})
	require.Equal(t, 1.0, *agg.AICredits)
	require.Equal(t, 1.0, *agg.ModelMetrics["a"].AICredits)
	require.Equal(t, 0.0, *agg.ModelMetrics["b"].AICredits)
}
