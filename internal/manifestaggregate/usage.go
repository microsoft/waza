package manifestaggregate

import (
	"math"

	"github.com/microsoft/waza/internal/models"
)

func sumRecordedUsage(stats []*models.UsageStats) (*models.UsageStats, bool) {
	if len(stats) == 0 {
		return nil, false
	}
	totals := [5]int{}
	modelsTotals := map[string][4]int{}
	for _, usage := range stats {
		if usage == nil {
			return nil, false
		}
		for i, value := range [5]int{usage.Turns, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens} {
			if value < 0 || totals[i] > math.MaxInt-value {
				return nil, false
			}
			totals[i] += value
		}
		for name, metrics := range usage.ModelMetrics {
			total := modelsTotals[name]
			for i, value := range [4]int{metrics.InputTokens, metrics.OutputTokens, metrics.CacheReadTokens, metrics.CacheWriteTokens} {
				if value < 0 || total[i] > math.MaxInt-value {
					return nil, false
				}
				total[i] += value
			}
			modelsTotals[name] = total
		}
	}
	result := models.AggregateUsageStats(stats)
	if result == nil {
		// Native recorded zero counters do not disappear merely because optional
		// credits/model attribution are unavailable.
		result = &models.UsageStats{}
	}
	valid := func(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 }
	if !valid(result.PremiumRequests) || (result.AICredits != nil && !valid(*result.AICredits)) {
		return nil, false
	}
	for _, metrics := range result.ModelMetrics {
		if !valid(metrics.RequestCount) || !valid(metrics.RequestCost) ||
			(metrics.AICredits != nil && !valid(*metrics.AICredits)) {
			return nil, false
		}
	}
	return result, true
}
