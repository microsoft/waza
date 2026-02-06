package models

import "time"

// EvaluationOutcome represents the complete result of an evaluation run
type EvaluationOutcome struct {
	RunID        string                   `json:"eval_id"`
	SkillTested  string                   `json:"skill"`
	BenchName    string                   `json:"eval_name"`
	Timestamp    time.Time                `json:"timestamp"`
	Setup        OutcomeSetup             `json:"config"`
	Digest       OutcomeDigest            `json:"summary"`
	Measures     map[string]MeasureResult `json:"metrics"`
	TestOutcomes []TestOutcome            `json:"tasks"`
	Metadata     map[string]any           `json:"metadata,omitempty"`
}

type OutcomeSetup struct {
	RunsPerTest int    `json:"runs_per_test"`
	ModelID     string `json:"model_id"`
	EngineType  string `json:"engine_type"`
	TimeoutSec  int    `json:"timeout_sec"`
}

type OutcomeDigest struct {
	TotalTests     int     `json:"total_tests"`
	Succeeded      int     `json:"succeeded"`
	Failed         int     `json:"failed"`
	Errors         int     `json:"errors"`
	Skipped        int     `json:"skipped"`
	SuccessRate    float64 `json:"success_rate"`
	AggregateScore float64 `json:"aggregate_score"`
	DurationMs     int64   `json:"duration_ms"`
}

type MeasureResult struct {
	Identifier string         `json:"identifier"`
	Value      float64        `json:"value"`
	Cutoff     float64        `json:"cutoff"`
	Passed     bool           `json:"passed"`
	Weight     float64        `json:"weight"`
	Details    map[string]any `json:"details,omitempty"`
}

// TestOutcome represents the result of one test case
type TestOutcome struct {
	TestID      string      `json:"test_id"`
	DisplayName string      `json:"display_name"`
	Status      string      `json:"status"`
	Runs        []RunResult `json:"runs"`
	Stats       *TestStats  `json:"stats,omitempty"`
}

// RunResult is the result of a single run/trial
type RunResult struct {
	RunNumber     int                      `json:"run_number"`
	Status        string                   `json:"status"`
	DurationMs    int64                    `json:"duration_ms"`
	Validations   map[string]ValidationOut `json:"validations"`
	SessionDigest SessionDigest            `json:"session_digest"`
	Transcript    []TranscriptEntry        `json:"transcript,omitempty"`
	FinalOutput   string                   `json:"final_output"`
	ErrorMsg      string                   `json:"error_msg,omitempty"`
}

type ValidationOut struct {
	Identifier string         `json:"identifier"`
	Kind       string         `json:"kind"`
	Score      float64        `json:"score"`
	Passed     bool           `json:"passed"`
	Feedback   string         `json:"feedback"`
	Details    map[string]any `json:"details,omitempty"`
	DurationMs int64          `json:"duration_ms"`
}

type SessionDigest struct {
	TotalTurns    int      `json:"total_turns"`
	ToolCallCount int      `json:"tool_call_count"`
	TokensIn      int      `json:"tokens_in"`
	TokensOut     int      `json:"tokens_out"`
	TokensTotal   int      `json:"tokens_total"`
	ToolsUsed     []string `json:"tools_used"`
	Errors        []string `json:"errors"`
}

type TranscriptEntry struct {
	Role    string         `json:"role"`
	Content string         `json:"content,omitempty"`
	Type    string         `json:"type,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

type TestStats struct {
	PassRate      float64 `json:"pass_rate"`
	AvgScore      float64 `json:"avg_score"`
	MinScore      float64 `json:"min_score"`
	MaxScore      float64 `json:"max_score"`
	AvgDurationMs int64   `json:"avg_duration_ms"`
}

// ComputeRunScore calculates the average score across all validations
func (r *RunResult) ComputeRunScore() float64 {
	if len(r.Validations) == 0 {
		return 0.0
	}
	total := 0.0
	for _, v := range r.Validations {
		total += v.Score
	}
	return total / float64(len(r.Validations))
}

// AllValidationsPassed checks if all validations passed
func (r *RunResult) AllValidationsPassed() bool {
	for _, v := range r.Validations {
		if !v.Passed {
			return false
		}
	}
	return true
}
