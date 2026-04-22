package quality

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatTable(t *testing.T) {
	resp := &JudgeResponse{
		Dimensions: []DimensionResult{
			{Name: "clarity", Score: 4, Feedback: "Clear instructions"},
			{Name: "completeness", Score: 3, Feedback: "Missing edge cases"},
			{Name: "trigger_precision", Score: 5, Feedback: "Perfect triggers"},
			{Name: "scope_coverage", Score: 2, Feedback: "Too broad"},
			{Name: "anti_patterns", Score: 4, Feedback: "Good patterns"},
		},
		OverallScore: 3.6,
		Summary:      "Decent skill with room to improve scope.",
	}

	output := FormatTable(resp)

	require.Contains(t, output, "DIMENSION")
	require.Contains(t, output, "SCORE")
	require.Contains(t, output, "FEEDBACK")
	require.Contains(t, output, "clarity")
	require.Contains(t, output, "completeness")
	require.Contains(t, output, "Overall: 3.6/5.0")
	require.Contains(t, output, "Decent skill")
	// Check score bars
	require.Contains(t, output, "████░") // score 4
	require.Contains(t, output, "███░░") // score 3
	require.Contains(t, output, "█████") // score 5
	require.Contains(t, output, "██░░░") // score 2
}

func TestFormatJSON(t *testing.T) {
	resp := &JudgeResponse{
		Dimensions: []DimensionResult{
			{Name: "clarity", Score: 4, Feedback: "Clear"},
		},
		OverallScore: 4.0,
		Summary:      "Good",
	}

	output, err := FormatJSON(resp)
	require.NoError(t, err)
	require.Contains(t, output, `"clarity"`)
	require.Contains(t, output, `"overall_score": 4`)
	require.Contains(t, output, `"summary": "Good"`)
}

func TestScoreBar(t *testing.T) {
	tests := []struct {
		score    int
		expected string
	}{
		{1, "█░░░░"},
		{2, "██░░░"},
		{3, "███░░"},
		{4, "████░"},
		{5, "█████"},
		{0, "█░░░░"}, // clamped to 1
		{6, "█████"}, // clamped to 5
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("score_%d", tt.score), func(t *testing.T) {
			require.Equal(t, tt.expected, scoreBar(tt.score))
		})
	}
}
