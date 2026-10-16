package models

import "encoding/json"

type GradeOutcome struct {
	OverallScore   float64                 `json:"overall_score"`
	Passed         bool                    `json:"passed"`
	Tasks          map[string]GradeOutcome `json:"tasks,omitempty"`
	GraderAverages map[string]float64      `json:"grader_averages,omitempty"`
	Status         Status                  `json:"status,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

func (g GradeOutcome) MarshalJSON() ([]byte, error) {
	type plain GradeOutcome
	var score *float64
	if g.Status != StatusError {
		score = &g.OverallScore
	}
	return json.Marshal(struct {
		plain
		OverallScore *float64 `json:"overall_score,omitempty"`
	}{plain(g), score})
}
