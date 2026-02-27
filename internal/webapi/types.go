package webapi

import "time"

// RunSummary is the API response for a single run in the list.
type RunSummary struct {
	ID         string    `json:"id"`
	Spec       string    `json:"spec"`
	Model      string    `json:"model"`
	JudgeModel string    `json:"judgeModel,omitempty"`
	Outcome    string    `json:"outcome"`
	PassCount  int       `json:"passCount"`
	TaskCount  int       `json:"taskCount"`
	Tokens     int       `json:"tokens"`
	Cost       float64   `json:"cost"`
	Duration   float64   `json:"duration"`
	Timestamp  time.Time `json:"timestamp"`
	Source     string    `json:"source,omitempty"` // "local" or "azure-blob"
}

// RunDetail is the API response for a single run with per-task results.
type RunDetail struct {
	RunSummary
	Tasks []TaskResult `json:"tasks"`
}

// TaskResult is a per-task result within a run.
type TaskResult struct {
	Name          string                      `json:"name"`
	Outcome       string                      `json:"outcome"`
	Score         float64                     `json:"score"`
	Duration      float64                     `json:"duration"`
	GraderResults []GraderResult              `json:"graderResults"`
	Transcript    []TranscriptEventResponse   `json:"transcript,omitempty"`
	SessionDigest *SessionDigestResponse      `json:"sessionDigest,omitempty"`
	BootstrapCI   *ConfidenceIntervalResponse `json:"bootstrapCI,omitempty"`
	IsSignificant *bool                       `json:"isSignificant,omitempty"`
}

// ConfidenceIntervalResponse is the API representation of a bootstrap CI.
type ConfidenceIntervalResponse struct {
	Lower           float64 `json:"lower"`
	Upper           float64 `json:"upper"`
	Mean            float64 `json:"mean"`
	ConfidenceLevel float64 `json:"confidenceLevel"`
}

// TranscriptEventResponse is the API representation of a transcript event.
type TranscriptEventResponse struct {
	Type       string `json:"type"`
	Content    string `json:"content,omitempty"`
	Message    string `json:"message,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Arguments  any    `json:"arguments,omitempty"`
	ToolResult any    `json:"toolResult,omitempty"`
	Success    *bool  `json:"success,omitempty"`
}

// SessionDigestResponse is the API representation of a session digest.
type SessionDigestResponse struct {
	TotalTurns    int      `json:"totalTurns"`
	ToolCallCount int      `json:"toolCallCount"`
	TokensIn      int      `json:"tokensIn"`
	TokensOut     int      `json:"tokensOut"`
	TokensTotal   int      `json:"tokensTotal"`
	ToolsUsed     []string `json:"toolsUsed"`
	Errors        []string `json:"errors"`
}

// GraderResult is a single grader/validator result.
type GraderResult struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Passed  bool    `json:"passed"`
	Score   float64 `json:"score"`
	Weight  float64 `json:"weight"`
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

// StorageStatusResponse is the storage configuration status.
type StorageStatusResponse struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider,omitempty"`
	Account    string `json:"account,omitempty"`
}
