package models

type GradeOutcome struct {
	OverallScore   float64                 `json:"overall_score"`
	Passed         bool                    `json:"passed"`
	Tasks          map[string]GradeOutcome `json:"tasks,omitempty"`
	GraderAverages map[string]float64      `json:"grader_averages,omitempty"`
}
