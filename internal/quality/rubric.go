package quality

// Dimension represents a single quality dimension in the rubric.
type Dimension struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	MinScore    int    `json:"min_score"`
	MaxScore    int    `json:"max_score"`
}

// DimensionResult holds a judge's score and feedback for one dimension.
type DimensionResult struct {
	Name     string `json:"name"`
	Score    int    `json:"score"`
	Feedback string `json:"feedback"`
}

// JudgeResponse is the structured response expected from the LLM judge.
type JudgeResponse struct {
	Dimensions   []DimensionResult `json:"dimensions"`
	OverallScore float64           `json:"overall_score"`
	Summary      string            `json:"summary"`
}

// DefaultRubric returns the built-in quality dimensions.
func DefaultRubric() []Dimension {
	return []Dimension{
		{
			Name:        "clarity",
			Description: "How clear and unambiguous are the instructions? Is the purpose immediately obvious? Are steps well-ordered and easy to follow?",
			MinScore:    1,
			MaxScore:    5,
		},
		{
			Name:        "completeness",
			Description: "Does the skill cover all necessary aspects? Are edge cases addressed? Is there enough detail for the agent to succeed?",
			MinScore:    1,
			MaxScore:    5,
		},
		{
			Name:        "trigger_precision",
			Description: "Are USE FOR and DO NOT USE FOR triggers well-defined? Do they avoid overlap? Would they correctly route requests?",
			MinScore:    1,
			MaxScore:    5,
		},
		{
			Name:        "scope_coverage",
			Description: "Does the skill define clear boundaries? Are capabilities and limitations explicit? Is the scope neither too broad nor too narrow?",
			MinScore:    1,
			MaxScore:    5,
		},
		{
			Name:        "anti_patterns",
			Description: "Does the skill avoid common anti-patterns such as vague instructions, conflicting directives, missing error handling guidance, or overly prescriptive steps?",
			MinScore:    1,
			MaxScore:    5,
		},
	}
}

// ValidateDimensionResult checks that a DimensionResult matches the rubric.
func ValidateDimensionResult(result DimensionResult, rubric []Dimension) bool {
	for _, d := range rubric {
		if d.Name == result.Name {
			return result.Score >= d.MinScore && result.Score <= d.MaxScore
		}
	}
	return false
}

// ValidateJudgeResponse checks that the response has all expected dimensions with valid scores.
func ValidateJudgeResponse(resp *JudgeResponse, rubric []Dimension) []string {
	var issues []string

	dimMap := make(map[string]bool, len(resp.Dimensions))
	for _, d := range resp.Dimensions {
		dimMap[d.Name] = true
	}

	for _, rd := range rubric {
		if !dimMap[rd.Name] {
			issues = append(issues, "missing dimension: "+rd.Name)
		}
	}

	for _, d := range resp.Dimensions {
		if !ValidateDimensionResult(d, rubric) {
			issues = append(issues, "invalid score for "+d.Name)
		}
	}

	if resp.OverallScore < 1.0 || resp.OverallScore > 5.0 {
		issues = append(issues, "overall_score must be between 1.0 and 5.0")
	}

	return issues
}
