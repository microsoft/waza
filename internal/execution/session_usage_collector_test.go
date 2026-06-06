package execution

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestSessionUsageCollector_UsageFromShutdown(t *testing.T) {
	coll := NewSessionUsageCollector()

	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionShutdownData{
			TotalPremiumRequests: copilot.Float64(5),
			ModelMetrics: map[string]copilot.ShutdownModelMetric{
				"claude-sonnet-4": {
					Usage: copilot.ShutdownModelMetricUsage{
						InputTokens:      1000,
						OutputTokens:     500,
						CacheReadTokens:  200,
						CacheWriteTokens: 100,
					},
					Requests: copilot.ShutdownModelMetricRequests{
						Count: utils.Ptr(int64(3)),
						Cost:  copilot.Float64(3),
					},
				},
				"gpt-4o": {
					Usage: copilot.ShutdownModelMetricUsage{
						InputTokens:  800,
						OutputTokens: 300,
					},
					Requests: copilot.ShutdownModelMetricRequests{
						Count: utils.Ptr(int64(2)),
						Cost:  copilot.Float64(2),
					},
				},
			},
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 5.0, usage.PremiumRequests)
	require.Equal(t, 1800, usage.InputTokens)
	require.Equal(t, 800, usage.OutputTokens)
	require.Equal(t, 200, usage.CacheReadTokens)
	require.Equal(t, 100, usage.CacheWriteTokens)
	require.Equal(t, 2600, usage.InputTokens+usage.OutputTokens)
	require.Len(t, usage.ModelMetrics, 2)

	require.Equal(t, models.ModelUsage{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
		RequestCount:     3,
		RequestCost:      3,
	}, usage.ModelMetrics["claude-sonnet-4"])
}

func TestSessionUsageCollector_UsageFromAssistantUsage(t *testing.T) {
	coll := NewSessionUsageCollector()

	in1, out1, cost1 := int64(500), int64(200), float64(1)
	coll.On(copilot.SessionEvent{
		Data: &copilot.AssistantUsageData{
			InputTokens:  &in1,
			OutputTokens: &out1,
			Cost:         &cost1,
			Model:        "gpt-4o",
		},
	})

	in2, out2, cost2 := int64(300), int64(100), float64(1)
	coll.On(copilot.SessionEvent{
		Data: &copilot.AssistantUsageData{
			InputTokens:  &in2,
			OutputTokens: &out2,
			Cost:         &cost2,
			Model:        "gpt-4o",
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 800, usage.InputTokens)
	require.Equal(t, 300, usage.OutputTokens)
	require.Equal(t, 2.0, usage.PremiumRequests)
}

func TestSessionUsageCollector_NoUsageReturnsNil(t *testing.T) {
	coll := NewSessionUsageCollector()

	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionIdleData{},
	})

	require.Nil(t, coll.UsageStats())
}

func TestSessionUsageCollector_ShutdownOverridesTurnUsage(t *testing.T) {
	coll := NewSessionUsageCollector()

	// Per-turn usage first
	in1, out1 := int64(500), int64(200)
	coll.On(copilot.SessionEvent{
		Data: &copilot.AssistantUsageData{
			InputTokens:  &in1,
			OutputTokens: &out1,
			Model:        "gpt-4o",
		},
	})

	// Shutdown event with authoritative totals should override
	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionShutdownData{
			TotalPremiumRequests: copilot.Float64(3),
			ModelMetrics: map[string]copilot.ShutdownModelMetric{
				"gpt-4o": {
					Usage: copilot.ShutdownModelMetricUsage{
						InputTokens:  1200,
						OutputTokens: 600,
					},
					Requests: copilot.ShutdownModelMetricRequests{Count: utils.Ptr(int64(3)), Cost: copilot.Float64(3)},
				},
			},
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	// ModelMetrics totals should be used, not the accumulated per-turn values
	require.Equal(t, 1200, usage.InputTokens)
	require.Equal(t, 600, usage.OutputTokens)
	require.Equal(t, 3.0, usage.PremiumRequests)
}

func TestSessionUsageCollector_SessionErrorCapturesUsage(t *testing.T) {
	coll := NewSessionUsageCollector()

	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionShutdownData{
			ShutdownType:         copilot.ShutdownType("error"),
			TotalPremiumRequests: copilot.Float64(2),
			ModelMetrics: map[string]copilot.ShutdownModelMetric{
				"gpt-4o": {
					Usage: copilot.ShutdownModelMetricUsage{
						InputTokens:  400,
						OutputTokens: 100,
					},
					Requests: copilot.ShutdownModelMetricRequests{Count: utils.Ptr(int64(2)), Cost: copilot.Float64(2)},
				},
			},
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 2.0, usage.PremiumRequests)
	require.Equal(t, 400, usage.InputTokens)
}

func TestSessionUsageCollector_TurnsFromAssistantTurnStart(t *testing.T) {
	coll := NewSessionUsageCollector()

	// Send three AssistantTurnStart events
	for range 3 {
		coll.On(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
	}

	// Also send a session-level event so UsageStats() returns non-nil
	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionShutdownData{TotalPremiumRequests: copilot.Float64(1)},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.Turns)
}

func TestSessionUsageCollector_TurnsWithTurnUsageFallback(t *testing.T) {
	coll := NewSessionUsageCollector()

	// AssistantTurnStart events increment the counter
	coll.On(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
	coll.On(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})

	// Per-turn usage (no session-level event) triggers fallback path
	in := int64(100)
	coll.On(copilot.SessionEvent{
		Data: &copilot.AssistantUsageData{InputTokens: &in, Model: "gpt-4o"},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.Turns)
	require.Equal(t, 100, usage.InputTokens)
}

func TestSessionUsageCollector_PremiumRequestsOnlyFallsBackToTurnTokens(t *testing.T) {
	coll := NewSessionUsageCollector()

	in1, out1 := int64(500), int64(200)
	coll.On(copilot.SessionEvent{
		Data: &copilot.AssistantUsageData{
			InputTokens:  &in1,
			OutputTokens: &out1,
			Model:        "gpt-4o",
		},
	})

	coll.On(copilot.SessionEvent{
		Data: &copilot.SessionShutdownData{TotalPremiumRequests: copilot.Float64(3)},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 3.0, usage.PremiumRequests)
	require.Equal(t, 500, usage.InputTokens)
	require.Equal(t, 200, usage.OutputTokens)
}
