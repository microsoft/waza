package models

import (
	"encoding/json"
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

func TestAggregateUsageStats_PreservesCustomProviderWhenConsistent(t *testing.T) {
	stats := []*UsageStats{
		{
			InputTokens:     100,
			PremiumRequests: 1,
			Provider:        UsageProviderCustom,
			ProviderHost:    "waza-test-resource.openai.azure.com",
		},
		{
			OutputTokens:    50,
			PremiumRequests: 1,
			Provider:        UsageProviderCustom,
			ProviderHost:    "waza-test-resource.openai.azure.com",
		},
	}

	agg := AggregateUsageStats(stats)
	require.NotNil(t, agg)
	require.Equal(t, UsageProviderCustom, agg.Provider)
	require.Equal(t, "waza-test-resource.openai.azure.com", agg.ProviderHost)
}

func TestAggregateUsageStats_MarksProviderMixedWhenInconsistent(t *testing.T) {
	stats := []*UsageStats{
		{
			InputTokens:     100,
			PremiumRequests: 1,
		},
		{
			OutputTokens:    50,
			PremiumRequests: 1,
			Provider:        UsageProviderCustom,
			ProviderHost:    "waza-test-resource.openai.azure.com",
		},
	}

	agg := AggregateUsageStats(stats)
	require.NotNil(t, agg)
	require.Equal(t, UsageProviderMixed, agg.Provider)
	require.Empty(t, agg.ProviderHost)
}

func TestAggregateUsageStats_AllNil(t *testing.T) {
	require.Nil(t, AggregateUsageStats([]*UsageStats{nil, nil}))
}

func TestAggregateUsageStats_Empty(t *testing.T) {
	require.Nil(t, AggregateUsageStats(nil))
}

func TestResponderInfoSerializes(t *testing.T) {
	rr := RunResult{
		RunNumber: 1,
		Status:    StatusError,
		Responder: &ResponderInfo{
			FollowupsSent: 3,
			Outcome:       "abstained",
			Reason:        "brief too vague",
		},
	}
	data, err := json.Marshal(rr)
	require.NoError(t, err)
	require.Contains(t, string(data), `"responder"`)
	require.Contains(t, string(data), `"outcome":"abstained"`)

	data2, err := json.Marshal(RunResult{RunNumber: 1, Status: StatusPassed})
	require.NoError(t, err)
	require.NotContains(t, string(data2), `"responder"`)
}

// TestHydrateToolCallArgsFromEvents_BackfillsExtra proves the offline grade
// path recovers MCP-style args from tool_events when SessionDigest was
// written by an older waza version that dropped ToolCallArgs.Extra
// (issue #474).
func TestHydrateToolCallArgsFromEvents_BackfillsExtra(t *testing.T) {
	run := &RunResult{
		SessionDigest: SessionDigest{
			ToolCalls: []ToolCall{
				{ID: "call-1", Name: "mcp_search", Arguments: ToolCallArgs{}},
				{ID: "call-2", Name: "mcp_search", Arguments: ToolCallArgs{Extra: map[string]any{"query": "already-here"}}},
				{ID: "", Name: "no-id", Arguments: ToolCallArgs{}},
			},
		},
		ToolEvents: []ToolEvent{
			{ToolCallID: "call-1", ToolName: "mcp_search", Args: map[string]any{
				"query": "hydrated",
				"limit": float64(3),
				// Collides with a known field: must be dropped from Extra.
				"path": "/should/not/overwrite",
			}},
			{ToolCallID: "call-2", ToolName: "mcp_search", Args: map[string]any{
				"query": "should-not-overwrite",
			}},
			// Args as a non-object value (some engines emit scalars) is skipped.
			{ToolCallID: "call-3", ToolName: "scalar", Args: "raw-string"},
		},
	}

	run.HydrateToolCallArgsFromEvents()

	require.Equal(t, "hydrated", run.SessionDigest.ToolCalls[0].Arguments.Extra["query"])
	require.EqualValues(t, 3, run.SessionDigest.ToolCalls[0].Arguments.Extra["limit"])
	_, hasPath := run.SessionDigest.ToolCalls[0].Arguments.Extra["path"]
	require.False(t, hasPath, "collision with known field must be dropped from Extra")

	// Never overwrite when Extra is already populated (live run wins).
	require.Equal(t, "already-here", run.SessionDigest.ToolCalls[1].Arguments.Extra["query"])

	// Empty ID => no hydration attempted.
	require.Empty(t, run.SessionDigest.ToolCalls[2].Arguments.Extra)
}

// TestHydrateToolCallArgsFromEvents_NoOpWhenEmpty guards the fast paths so
// live runs and pre-1.1 results.json files aren't perturbed.
func TestHydrateToolCallArgsFromEvents_NoOpWhenEmpty(t *testing.T) {
	// Nil receiver must not panic.
	var nilRun *RunResult
	nilRun.HydrateToolCallArgsFromEvents()

	// No ToolEvents => nothing to hydrate from.
	run := &RunResult{SessionDigest: SessionDigest{ToolCalls: []ToolCall{{ID: "x"}}}}
	run.HydrateToolCallArgsFromEvents()
	require.Empty(t, run.SessionDigest.ToolCalls[0].Arguments.Extra)

	// No ToolCalls => nothing to update.
	run2 := &RunResult{ToolEvents: []ToolEvent{{ToolCallID: "x", Args: map[string]any{"a": 1}}}}
	require.NotPanics(t, func() { run2.HydrateToolCallArgsFromEvents() })
}
