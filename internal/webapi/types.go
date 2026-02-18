package webapi

import "time"

// RunSummary is the API response for a single run in the list.
type RunSummary struct {
	ID        string    `json:"id"`
	Spec      string    `json:"spec"`
	Model     string    `json:"model"`
	Outcome   string    `json:"outcome"`
	PassCount int       `json:"passCount"`
	TaskCount int       `json:"taskCount"`
	Tokens    int       `json:"tokens"`
	Cost      float64   `json:"cost"`
	Duration  float64   `json:"duration"`
	Timestamp time.Time `json:"timestamp"`
}

// RunDetail is the API response for a single run with per-task results.
type RunDetail struct {
	RunSummary
	Tasks []TaskResult `json:"tasks"`
}

// TaskResult is a per-task result within a run.
type TaskResult struct {
	Name          string         `json:"name"`
	Outcome       string         `json:"outcome"`
	Score         float64        `json:"score"`
	Duration      float64        `json:"duration"`
	GraderResults []GraderResult `json:"graderResults"`
}

// GraderResult is a single grader/validator result.
type GraderResult struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Passed  bool    `json:"passed"`
	Score   float64 `json:"score"`
	Message string  `json:"message"`
}

// SummaryResponse is the aggregate KPI response.
type SummaryResponse struct {
	TotalRuns   int     `json:"totalRuns"`
	TotalTasks  int     `json:"totalTasks"`
	PassRate    float64 `json:"passRate"`
	AvgTokens   float64 `json:"avgTokens"`
	AvgCost     float64 `json:"avgCost"`
	AvgDuration float64 `json:"avgDuration"`
}

// HealthResponse is the health check response.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// ErrorResponse is returned for errors.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}
