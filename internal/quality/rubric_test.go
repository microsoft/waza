package quality

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultRubric(t *testing.T) {
	rubric := DefaultRubric()
	require.Len(t, rubric, 5)

	names := make(map[string]bool)
	for _, d := range rubric {
		names[d.Name] = true
		require.Equal(t, 1, d.MinScore)
		require.Equal(t, 5, d.MaxScore)
		require.NotEmpty(t, d.Description)
	}

	require.True(t, names["clarity"])
	require.True(t, names["completeness"])
	require.True(t, names["trigger_precision"])
	require.True(t, names["scope_coverage"])
	require.True(t, names["anti_patterns"])
}

func TestValidateDimensionResult(t *testing.T) {
	rubric := DefaultRubric()

	tests := []struct {
		name  string
		dim   DimensionResult
		valid bool
	}{
		{"valid clarity", DimensionResult{Name: "clarity", Score: 3}, true},
		{"min score", DimensionResult{Name: "clarity", Score: 1}, true},
		{"max score", DimensionResult{Name: "clarity", Score: 5}, true},
		{"too low", DimensionResult{Name: "clarity", Score: 0}, false},
		{"too high", DimensionResult{Name: "clarity", Score: 6}, false},
		{"unknown dimension", DimensionResult{Name: "unknown", Score: 3}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateDimensionResult(tt.dim, rubric)
			require.Equal(t, tt.valid, result)
		})
	}
}

func TestValidateJudgeResponse_Valid(t *testing.T) {
	rubric := DefaultRubric()
	resp := &JudgeResponse{
		Dimensions: []DimensionResult{
			{Name: "clarity", Score: 4, Feedback: "Clear"},
			{Name: "completeness", Score: 3, Feedback: "Missing edge cases"},
			{Name: "trigger_precision", Score: 5, Feedback: "Excellent triggers"},
			{Name: "scope_coverage", Score: 4, Feedback: "Well-bounded"},
			{Name: "anti_patterns", Score: 3, Feedback: "Some vague steps"},
		},
		OverallScore: 3.8,
		Summary:      "Good skill overall",
	}

	issues := ValidateJudgeResponse(resp, rubric)
	require.Empty(t, issues)
}

func TestValidateJudgeResponse_MissingDimension(t *testing.T) {
	rubric := DefaultRubric()
	resp := &JudgeResponse{
		Dimensions: []DimensionResult{
			{Name: "clarity", Score: 4, Feedback: "Clear"},
		},
		OverallScore: 4.0,
		Summary:      "Incomplete",
	}

	issues := ValidateJudgeResponse(resp, rubric)
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0], "missing dimension")
}

func TestValidateJudgeResponse_InvalidOverallScore(t *testing.T) {
	rubric := DefaultRubric()
	resp := &JudgeResponse{
		Dimensions: []DimensionResult{
			{Name: "clarity", Score: 4, Feedback: "Clear"},
			{Name: "completeness", Score: 3, Feedback: "OK"},
			{Name: "trigger_precision", Score: 5, Feedback: "Good"},
			{Name: "scope_coverage", Score: 4, Feedback: "OK"},
			{Name: "anti_patterns", Score: 3, Feedback: "OK"},
		},
		OverallScore: 6.0,
		Summary:      "Invalid score",
	}

	issues := ValidateJudgeResponse(resp, rubric)
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0], "overall_score")
}
