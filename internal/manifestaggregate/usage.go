package manifestaggregate

import (
	"math"

	"github.com/microsoft/waza/internal/models"
)

const maxJSONInteger int64 = (1 << 53) - 1

func sumRecordedUsage(stats []*models.UsageStats) (*models.UsageStats, string) {
	if len(stats) == 0 {
		return nil, "complete_historical_usage_unavailable"
	}
	totals := [5]int{}
	modelsTotals := map[string][4]int{}
	floatTotals := [2]float64{}
	modelFloatTotals := map[string][3]float64{}
	validFloat := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= float64(maxJSONInteger)
	}
	validCredits := func(value *float64) bool { return value == nil || validFloat(*value) }
	addFloat := func(total *float64, value float64) bool {
		sum := *total + value
		if !validFloat(value) || !validFloat(sum) ||
			(*total > 0 && value > 0 && (sum == *total || sum == value)) {
			return false
		}
		*total = sum
		return true
	}
	for _, usage := range stats {
		if usage == nil {
			return nil, "complete_historical_usage_unavailable"
		}
		if !addFloat(&floatTotals[0], usage.PremiumRequests) ||
			(usage.AICredits != nil && !addFloat(&floatTotals[1], *usage.AICredits)) {
			return nil, "recorded_usage_numeric_precision_unavailable"
		}
		for i, value := range [5]int{usage.Turns, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens} {
			if value < 0 || totals[i] > math.MaxInt-value {
				return nil, "recorded_usage_numeric_overflow"
			}
			totals[i] += value
			if int64(totals[i]) > maxJSONInteger {
				return nil, "recorded_usage_numeric_precision_unavailable"
			}
		}
		for name, metrics := range usage.ModelMetrics {
			floats := modelFloatTotals[name]
			if !addFloat(&floats[0], metrics.RequestCount) || !addFloat(&floats[1], metrics.RequestCost) ||
				(metrics.AICredits != nil && !addFloat(&floats[2], *metrics.AICredits)) {
				return nil, "recorded_usage_numeric_precision_unavailable"
			}
			modelFloatTotals[name] = floats
			total := modelsTotals[name]
			for i, value := range [4]int{metrics.InputTokens, metrics.OutputTokens, metrics.CacheReadTokens, metrics.CacheWriteTokens} {
				if value < 0 || total[i] > math.MaxInt-value {
					return nil, "recorded_usage_numeric_overflow"
				}
				total[i] += value
				if int64(total[i]) > maxJSONInteger {
					return nil, "recorded_usage_numeric_precision_unavailable"
				}
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
	if !validFloat(result.PremiumRequests) || !validCredits(result.AICredits) {
		return nil, "recorded_usage_numeric_precision_unavailable"
	}
	for _, metrics := range result.ModelMetrics {
		if !validFloat(metrics.RequestCount) || !validFloat(metrics.RequestCost) || !validCredits(metrics.AICredits) {
			return nil, "recorded_usage_numeric_precision_unavailable"
		}
	}
	return result, ""
}
