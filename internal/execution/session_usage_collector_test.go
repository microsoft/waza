package execution

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSessionUsageCollector_UsageFromShutdown(t *testing.T) {
	coll := NewSessionUsageCollector()

	premReqs := float64(5)
	coll.On(copilot.SessionEvent{
		Type: copilot.SessionIdle,
		Data: copilot.Data{
			TotalPremiumRequests: &premReqs,
			ModelMetrics: map[string]copilot.ModelMetric{
				"claude-sonnet-4": {
					Usage: copilot.Usage{
						InputTokens:      1000,
						OutputTokens:     500,
						CacheReadTokens:  200,
						CacheWriteTokens: 100,
					},
					Requests: copilot.Requests{
						Count: 3,
						Cost:  3,
					},
				},
				"gpt-4o": {
					Usage: copilot.Usage{
						InputTokens:  800,
						OutputTokens: 300,
					},
					Requests: copilot.Requests{
						Count: 2,
						Cost:  2,
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

	in1, out1, cost1 := float64(500), float64(200), float64(1)
	coll.On(copilot.SessionEvent{
		Type: copilot.AssistantUsage,
		Data: copilot.Data{
			InputTokens:  &in1,
			OutputTokens: &out1,
			Cost:         &cost1,
		},
	})

	in2, out2, cost2 := float64(300), float64(100), float64(1)
	coll.On(copilot.SessionEvent{
		Type: copilot.AssistantUsage,
		Data: copilot.Data{
			InputTokens:  &in2,
			OutputTokens: &out2,
			Cost:         &cost2,
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
		Type: copilot.SessionIdle,
		Data: copilot.Data{},
	})

	require.Nil(t, coll.UsageStats())
}

func TestSessionUsageCollector_ShutdownOverridesTurnUsage(t *testing.T) {
	coll := NewSessionUsageCollector()

	// Per-turn usage first
	in1, out1 := float64(500), float64(200)
	coll.On(copilot.SessionEvent{
		Type: copilot.AssistantUsage,
		Data: copilot.Data{
			InputTokens:  &in1,
			OutputTokens: &out1,
		},
	})

	// Shutdown event with authoritative totals should override
	premReqs := float64(3)
	coll.On(copilot.SessionEvent{
		Type: copilot.SessionIdle,
		Data: copilot.Data{
			TotalPremiumRequests: &premReqs,
			ModelMetrics: map[string]copilot.ModelMetric{
				"gpt-4o": {
					Usage: copilot.Usage{
						InputTokens:  1200,
						OutputTokens: 600,
					},
					Requests: copilot.Requests{Count: 3, Cost: 3},
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

	premReqs := float64(2)
	coll.On(copilot.SessionEvent{
		Type: copilot.SessionError,
		Data: copilot.Data{
			TotalPremiumRequests: &premReqs,
			ModelMetrics: map[string]copilot.ModelMetric{
				"gpt-4o": {
					Usage: copilot.Usage{
						InputTokens:  400,
						OutputTokens: 100,
					},
					Requests: copilot.Requests{Count: 2, Cost: 2},
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
		coll.On(copilot.SessionEvent{Type: copilot.AssistantTurnStart})
	}

	// Also send a session-level event so UsageStats() returns non-nil
	premReqs := float64(1)
	coll.On(copilot.SessionEvent{
		Type: copilot.SessionIdle,
		Data: copilot.Data{
			TotalPremiumRequests: &premReqs,
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.Turns)
}

func TestSessionUsageCollector_TurnsWithTurnUsageFallback(t *testing.T) {
	coll := NewSessionUsageCollector()

	// AssistantTurnStart events increment the counter
	coll.On(copilot.SessionEvent{Type: copilot.AssistantTurnStart})
	coll.On(copilot.SessionEvent{Type: copilot.AssistantTurnStart})

	// Per-turn usage (no session-level event) triggers fallback path
	in := float64(100)
	coll.On(copilot.SessionEvent{
		Type: copilot.AssistantUsage,
		Data: copilot.Data{InputTokens: &in},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.Turns)
	require.Equal(t, 100, usage.InputTokens)
}

func TestSessionUsageCollector_PremiumRequestsOnlyFallsBackToTurnTokens(t *testing.T) {
	coll := NewSessionUsageCollector()

	in1, out1 := float64(500), float64(200)
	coll.On(copilot.SessionEvent{
		Type: copilot.AssistantUsage,
		Data: copilot.Data{
			InputTokens:  &in1,
			OutputTokens: &out1,
		},
	})

	premReqs := float64(3)
	coll.On(copilot.SessionEvent{
		Type: copilot.SessionIdle,
		Data: copilot.Data{
			TotalPremiumRequests: &premReqs,
		},
	})

	usage := coll.UsageStats()
	require.NotNil(t, usage)
	require.Equal(t, 3.0, usage.PremiumRequests)
	require.Equal(t, 500, usage.InputTokens)
	require.Equal(t, 200, usage.OutputTokens)
}
