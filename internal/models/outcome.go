package models

import (
	"math"
	"time"
)

// Status represents the outcome status of a test or run.
type Status string

const (
	StatusPassed Status = "passed"
	StatusFailed Status = "failed"
	StatusError  Status = "error"
	// StatusNA is used in comparison reports when a task is not found in a result file.
	StatusNA Status = "n/a"
)

// GraderKind identifies the type of grader (e.g. regex, file, code).
type GraderKind string

const (
	GraderKindInlineScript    GraderKind = "code"
	GraderKindPrompt          GraderKind = "prompt"
	GraderKindRegex           GraderKind = "regex"
	GraderKindFile            GraderKind = "file"
	GraderKindKeyword         GraderKind = "keyword"
	GraderKindJSONSchema      GraderKind = "json_schema"
	GraderKindProgram         GraderKind = "program"
	GraderKindBehavior        GraderKind = "behavior"
	GraderKindActionSequence  GraderKind = "action_sequence"
	GraderKindSkillInvocation GraderKind = "skill_invocation"
)

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
	MinScore       float64 `json:"min_score"`
	MaxScore       float64 `json:"max_score"`
	StdDev         float64 `json:"std_dev"`
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
	Status      Status      `json:"status"`
	Runs        []RunResult `json:"runs"`
	Stats       *TestStats  `json:"stats,omitempty"`
}

// RunResult is the result of a single run/trial
type RunResult struct {
	RunNumber int `json:"run_number"`
	// Status contains the overall status of the run.
	// NOTE: if Status == [StatusError], then [ErrorMsg] will be set to the
	// message from the error.
	Status        Status                   `json:"status"`
	DurationMs    int64                    `json:"duration_ms"`
	Validations   map[string]GraderResults `json:"validations"`
	SessionDigest SessionDigest            `json:"session_digest"`
	Transcript    []TranscriptEvent        `json:"transcript,omitempty"`
	FinalOutput   string                   `json:"final_output"`
	ErrorMsg      string                   `json:"error_msg,omitempty"`
}

type GraderResults struct {
	Name       string         `json:"identifier"`
	Type       GraderKind     `json:"type"`
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

type TestStats struct {
	PassRate      float64 `json:"pass_rate"`
	AvgScore      float64 `json:"avg_score"`
	MinScore      float64 `json:"min_score"`
	MaxScore      float64 `json:"max_score"`
	StdDevScore   float64 `json:"std_dev_score"`
	ScoreVariance float64 `json:"score_variance"`
	CI95Lo        float64 `json:"ci95_lo"`
	CI95Hi        float64 `json:"ci95_hi"`
	Flaky         bool    `json:"flaky"`
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

// ComputeStdDev returns the population standard deviation for a slice of float64 values.
func ComputeStdDev(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0.0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	variance := 0.0
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	return math.Sqrt(variance / float64(n))
}
