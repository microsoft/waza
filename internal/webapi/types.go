package webapi

import (
	"time"

	"github.com/microsoft/waza/internal/models"
)

// RunSummary is the API response for a single run in the list.
type RunSummary struct {
	ID              string  `json:"id"`
	Spec            string  `json:"spec"`
	Model           string  `json:"model"`
	JudgeModel      string  `json:"judgeModel,omitempty"`
	Outcome         string  `json:"outcome"`
	PassCount       int     `json:"passCount"`
	TaskCount       int     `json:"taskCount"`
	Tokens          int     `json:"tokens"`
	PremiumRequests float64 `json:"premiumRequests"`
	// AICredits is the final AI-credit total reported by the Copilot SDK for the
	// Copilot usage this run initiated. It is omitted for legacy artifacts and
	// runtimes that do not report final AI-credit metrics; clients must render
	// that as an unavailable state rather than substituting an estimate.
	AICredits *float64 `json:"aiCredits,omitempty"`
	// ModelUsage is the per-model usage breakdown for the run, sorted by model ID.
	ModelUsage []ModelUsageResponse `json:"modelUsage,omitempty"`
	Cost       float64              `json:"cost"`
	// CostSource records how Cost was computed: "sdk" (reported by the Copilot
	// SDK), "table" (priced from the embedded model rate table), or "estimate"
	// (flat-rate fallback). Empty for legacy summaries that carry no token/cost
	// data.
	CostSource    string    `json:"costSource,omitempty"`
	Duration      float64   `json:"duration"`
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source,omitempty"` // "local" or "azure-blob"
	Skill         string    `json:"skill,omitempty"`
	Repetitions   int       `json:"repetitions,omitempty"`
	WeightedScore *float64  `json:"weightedScore,omitempty"`
}

// RunDetail is the API response for a single run with per-task results.
type RunDetail struct {
	RunSummary
	Tasks []TaskResult `json:"tasks"`
}

// ModelUsageResponse is the per-model usage breakdown for a run. AICredits is
// omitted when the Copilot SDK did not report a final AI-credit total for the
// model.
type ModelUsageResponse struct {
	Model            string   `json:"model"`
	AICredits        *float64 `json:"aiCredits,omitempty"`
	InputTokens      int      `json:"inputTokens"`
	CacheReadTokens  int      `json:"cacheReadTokens"`
	CacheWriteTokens int      `json:"cacheWriteTokens"`
	OutputTokens     int      `json:"outputTokens"`
}

// TaskResult is a per-task result within a run.
type TaskResult struct {
	ID            string                      `json:"id,omitempty"`
	Name          string                      `json:"name"`
	Prompt        string                      `json:"prompt,omitempty"`
	Outcome       string                      `json:"outcome"`
	Score         float64                     `json:"score"`
	WeightedScore *float64                    `json:"weightedScore,omitempty"`
	Duration      float64                     `json:"duration"`
	GraderResults []GraderResult              `json:"graderResults"`
	Transcript    []TranscriptEventResponse   `json:"transcript,omitempty"`
	SessionDigest *SessionDigestResponse      `json:"sessionDigest,omitempty"`
	Responder     *ResponderInfoResponse      `json:"responder,omitempty"`
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
	ToolPolicyMode    string                    `json:"toolPolicyMode,omitempty"`
	ToolPolicyDenials []models.ToolPolicyDenial `json:"toolPolicyDenials,omitempty"`
	TotalTurns        int                       `json:"totalTurns"`
	ToolCallCount     int                       `json:"toolCallCount"`
	TokensIn          int                       `json:"tokensIn"`
	TokensOut         int                       `json:"tokensOut"`
	TokensTotal       int                       `json:"tokensTotal"`
	ToolsUsed         []string                  `json:"toolsUsed"`
	Errors            []string                  `json:"errors"`
}

// ResponderInfoResponse is the API representation of a responder-driven run summary.
type ResponderInfoResponse struct {
	FollowupsSent int    `json:"followupsSent"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
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
	TotalRuns          int     `json:"totalRuns"`
	TotalTasks         int     `json:"totalTasks"`
	PassRate           float64 `json:"passRate"`
	AvgTokens          float64 `json:"avgTokens"`
	AvgPremiumRequests float64 `json:"avgPremiumRequests"`
	// AvgAICredits is the mean final AI-credit total across the runs that
	// reported one. It is omitted when no run carries AI-credit metrics.
	AvgAICredits *float64 `json:"avgAICredits,omitempty"`
	AvgCost      float64  `json:"avgCost"`
	// CostSource records the source of AvgCost across runs: "sdk", "table",
	// "estimate", or "mixed" when different runs were priced from different
	// sources. Empty when there are no runs, or when every aggregated run lacks
	// cost data (e.g. legacy ResultSummary rows that don't carry token usage).
	CostSource  string  `json:"costSource,omitempty"`
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
