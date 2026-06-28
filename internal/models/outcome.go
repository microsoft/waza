package models

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/microsoft/waza/internal/statistics"
)

// Status represents the outcome status of a test or run.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
	// StatusNA is used in comparison reports when a task is not found in a result file.
	StatusNA Status = "n/a"
)

// Responder outcome values recorded on RunResult.Responder.Outcome.
const (
	ResponderOutcomeStopped      = "stopped"
	ResponderOutcomeAbstained    = "abstained"
	ResponderOutcomeCapExhausted = "cap_exhausted"
	ResponderOutcomeError        = "error"
)

// GraderKind identifies the type of grader (e.g. regex, file, code).
type GraderKind string

const (
	// NOTE: if you add more, make sure you add them to [AllGraderKinds], below.

	GraderKindInlineScript    GraderKind = "code"
	GraderKindPrompt          GraderKind = "prompt"
	GraderKindText            GraderKind = "text"
	GraderKindFile            GraderKind = "file"
	GraderKindJSONSchema      GraderKind = "json_schema"
	GraderKindProgram         GraderKind = "program"
	GraderKindBehavior        GraderKind = "behavior"
	GraderKindActionSequence  GraderKind = "action_sequence"
	GraderKindSkillInvocation GraderKind = "skill_invocation"
	GraderKindTrigger         GraderKind = "trigger"
	GraderKindDiff            GraderKind = "diff"
	GraderKindToolConstraint  GraderKind = "tool_constraint"
	GraderKindToolCalls       GraderKind = "tool_calls"
)

func AllGraderKinds() []string {
	names := []string{
		string(GraderKindInlineScript),
		string(GraderKindPrompt),
		string(GraderKindText),
		string(GraderKindFile),
		string(GraderKindJSONSchema),
		string(GraderKindProgram),
		string(GraderKindBehavior),
		string(GraderKindActionSequence),
		string(GraderKindSkillInvocation),
		string(GraderKindTrigger),
		string(GraderKindDiff),
		string(GraderKindToolConstraint),
		string(GraderKindToolCalls),
	}

	sort.Strings(names)
	return names
}

// ResponderInfo records the outcome of a responder-driven multi-turn run.
type ResponderInfo struct {
	// FollowupsSent is the number of responder answers sent to the agent.
	FollowupsSent int `json:"followups_sent"`
	// Outcome is one of: stopped, abstained, cap_exhausted, error.
	Outcome string `json:"outcome"`
	// Reason holds the responder's reason when Outcome == "abstained" or an
	// error message when Outcome == "error".
	Reason string `json:"reason,omitempty"`
}

// EvaluationOutcome represents the complete result of an evaluation run
type EvaluationOutcome struct {
	SchemaVersion   string                   `json:"schemaVersion"`
	RunID           string                   `json:"eval_id"`
	SkillTested     string                   `json:"skill"`
	BenchName       string                   `json:"eval_name"`
	Timestamp       time.Time                `json:"timestamp"`
	Setup           OutcomeSetup             `json:"config"`
	Digest          OutcomeDigest            `json:"summary"`
	Measures        map[string]MeasureResult `json:"metrics"`
	TestOutcomes    []TestOutcome            `json:"tasks"`
	TriggerMetrics  *TriggerMetrics          `json:"trigger_metrics,omitempty"`
	TriggerResults  []TriggerResult          `json:"trigger_results,omitempty"`
	Metadata        map[string]any           `json:"metadata,omitempty"`
	IsBaseline      bool                     `json:"is_baseline,omitempty"`
	BaselineOutcome *EvaluationOutcome       `json:"baseline_outcome,omitempty"`
}

func (o EvaluationOutcome) MarshalJSON() ([]byte, error) {
	type alias EvaluationOutcome
	if o.SchemaVersion == "" {
		o.SchemaVersion = CurrentSchemaVersion
	}
	return json.Marshal(alias(o))
}

type OutcomeSetup struct {
	RunsPerTest int    `json:"runs_per_test"`
	ModelID     string `json:"model_id"`
	EngineType  string `json:"engine_type"`
	TimeoutSec  int    `json:"timeout_sec"`
	JudgeModel  string `json:"judge_model,omitempty"`
}

type OutcomeDigest struct {
	TotalTests     int          `json:"total_tests"`
	Succeeded      int          `json:"succeeded"`
	Failed         int          `json:"failed"`
	Errors         int          `json:"errors"`
	Skipped        int          `json:"skipped"`
	SuccessRate    float64      `json:"success_rate"`
	AggregateScore float64      `json:"aggregate_score"`
	WeightedScore  float64      `json:"weighted_score"`
	MinScore       float64      `json:"min_score"`
	MaxScore       float64      `json:"max_score"`
	StdDev         float64      `json:"std_dev"`
	DurationMs     int64        `json:"duration_ms"`
	Groups         []GroupStats `json:"groups,omitempty"`
	Usage          *UsageStats  `json:"usage,omitempty"`

	// Statistical summary populated when trials_per_task > 1
	Statistics *StatisticalSummary `json:"statistics,omitempty"`
}

type MeasureResult struct {
	Identifier string         `json:"identifier"`
	Value      float64        `json:"value"`
	Threshold  float64        `json:"threshold"`
	Passed     bool           `json:"passed"`
	Weight     float64        `json:"weight"`
	Details    map[string]any `json:"details,omitempty"`
}

// TestOutcome represents the result of one test case
type TestOutcome struct {
	TestID      string `json:"test_id"`
	DisplayName string `json:"display_name"`
	Group       string `json:"group,omitempty"`
	// Golden mirrors TestCase.Golden so `waza gate` can identify
	// must-pass tasks directly from results.json without re-reading YAML.
	Golden      bool               `json:"golden,omitempty"`
	Status      Status             `json:"status"`
	Runs        []RunResult        `json:"runs"`
	Stats       *TestStats         `json:"stats,omitempty"`
	SkillImpact *SkillImpactMetric `json:"skill_impact,omitempty"`
}

// GroupStats holds aggregate statistics for a group of test outcomes.
type GroupStats struct {
	Name     string  `json:"name"`
	Passed   int     `json:"passed"`
	Total    int     `json:"total"`
	AvgScore float64 `json:"avg_score"`
}

// SkillInvocation records a skill invoked during an agent session.
type SkillInvocation struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

// RunResult is the result of a single run/trial
type RunResult struct {
	RunNumber int `json:"run_number"`
	Attempts  int `json:"attempts"`
	// Status contains the overall status of the run.
	// NOTE: if Status == [StatusError], then [ErrorMsg] will be set to the
	// message from the error.
	Status           Status                   `json:"status"`
	DurationMs       int64                    `json:"duration_ms"`
	Validations      map[string]GraderResults `json:"validations"`
	SessionDigest    SessionDigest            `json:"session_digest"`
	Transcript       []TranscriptEvent        `json:"transcript,omitempty"`
	FinalOutput      string                   `json:"final_output"`
	ErrorMsg         string                   `json:"error_msg,omitempty"`
	SkillInvocations []SkillInvocation        `json:"skill_invocations,omitempty"`
	Usage            *UsageStats              `json:"usage,omitempty"`
	WorkspaceDir     string                   `json:"workspace_dir,omitempty"`
	FailureArtifacts *FailureArtifacts        `json:"failure_artifacts,omitempty"`
	Responder        *ResponderInfo           `json:"responder,omitempty"`
	// Checkpoints captures per-turn checkpoint grader results, one entry per
	// configured TestCase.Checkpoint that actually ran (i.e., turn index was
	// reached). Empty / omitted when the task defines no checkpoints.
	Checkpoints []CheckpointOutcome `json:"checkpoints,omitempty"`

	// ToolEvents is a normalized, replay-friendly record of every tool call
	// made during the run. Added in schema version 1.1 as an additive field
	// (see issue #366). The legacy SessionDigest.ToolCalls is preserved for
	// backward compatibility; new consumers should prefer ToolEvents.
	ToolEvents []ToolEvent `json:"tool_events,omitempty"`
}

// CheckpointOutcome captures the results of a single TestCase.Checkpoint that
// ran during a multi-turn run. It mirrors the shape of the final-grader
// validations map so dashboards can present per-turn pass/fail uniformly.
type CheckpointOutcome struct {
	// AfterTurn echoes Checkpoint.AfterTurn so consumers can sort/group.
	AfterTurn int `json:"after_turn"`
	// Status is StatusPassed when every grader in this checkpoint passed,
	// StatusFailed when at least one grader failed.
	Status Status `json:"status"`
	// Validations maps grader identifier to result, identical to
	// RunResult.Validations.
	Validations map[string]GraderResults `json:"validations"`
	// Stopped is true when this checkpoint had `on_failure: stop` and at
	// least one grader failed, terminating the multi-turn loop after this
	// turn.
	Stopped bool `json:"stopped,omitempty"`
}

// FailureArtifacts captures diagnostic information when a run fails
type FailureArtifacts struct {
	StdErr        string            `json:"stderr,omitempty"`
	StdOut        string            `json:"stdout,omitempty"`
	ExitCode      int               `json:"exit_code,omitempty"`
	FailedGraders []string          `json:"failed_graders,omitempty"`
	ErrorPatterns []string          `json:"error_patterns,omitempty"`
	TriageSummary string            `json:"triage_summary,omitempty"`
	CapturedAt    time.Time         `json:"captured_at"`
	Context       map[string]string `json:"context,omitempty"`
}

type GraderResults struct {
	Name       string         `json:"identifier"`
	Type       GraderKind     `json:"type"`
	Score      float64        `json:"score"`
	Weight     float64        `json:"weight"`
	Passed     bool           `json:"passed"`
	Feedback   string         `json:"feedback"`
	Details    map[string]any `json:"details,omitempty"`
	DurationMs int64          `json:"duration_ms"`
}

type SessionDigest struct {
	ToolCallCount int         `json:"tool_call_count"`
	ToolsUsed     []string    `json:"tools_used"`
	ToolCalls     []ToolCall  `json:"tool_calls,omitempty"`
	Errors        []string    `json:"errors"`
	Usage         *UsageStats `json:"usage,omitempty"`
	SessionID     string      `json:"session_id,omitempty"`
}

const (
	// UsageProviderCustom indicates request counters came from a BYOK/custom provider.
	UsageProviderCustom = "custom"

	// UsageProviderMixed indicates aggregate usage spans different provider routes.
	UsageProviderMixed = "mixed"
)

// UsageStats holds token and premium request usage data from a Copilot SDK session.
//
// Provider is empty for the default Copilot MaaS path. When it is "custom",
// PremiumRequests should be read as custom-provider request count rather than
// GitHub Copilot premium billing. ProviderHost may contain a sanitized host
// from the custom provider base URL; it intentionally omits scheme, path, query,
// and credentials.
type UsageStats struct {
	Turns            int                   `json:"turns"`
	InputTokens      int                   `json:"input_tokens"`
	OutputTokens     int                   `json:"output_tokens"`
	CacheReadTokens  int                   `json:"cache_read_tokens"`
	CacheWriteTokens int                   `json:"cache_write_tokens"`
	PremiumRequests  float64               `json:"premium_requests"`
	Provider         string                `json:"provider,omitempty"`
	ProviderHost     string                `json:"provider_host,omitempty"`
	ModelMetrics     map[string]ModelUsage `json:"model_metrics,omitempty"`
}

// IsZero returns true if no usage data has been recorded.
func (u *UsageStats) IsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 &&
		u.PremiumRequests == 0 && u.Turns == 0
}

// ModelUsage holds per-model token and request usage.
type ModelUsage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	RequestCount     float64 `json:"request_count"`
	RequestCost      float64 `json:"request_cost"`
}

// AggregateUsageStats sums usage across multiple UsageStats (e.g. across runs).
// Provider metadata is preserved when all aggregated stats share the same route.
// If usage spans multiple provider routes, Provider is set to "mixed" so
// consumers avoid labeling the aggregate as purely Copilot premium or BYOK.
func AggregateUsageStats(stats []*UsageStats) *UsageStats {
	agg := &UsageStats{
		ModelMetrics: make(map[string]ModelUsage),
	}
	var provider string
	var providerHost string
	providerSet := false
	providerConsistent := true
	for _, s := range stats {
		if s == nil {
			continue
		}
		if !providerSet {
			provider = s.Provider
			providerHost = s.ProviderHost
			providerSet = true
		} else if provider != s.Provider || providerHost != s.ProviderHost {
			providerConsistent = false
		}
		agg.Turns += s.Turns
		agg.InputTokens += s.InputTokens
		agg.OutputTokens += s.OutputTokens
		agg.CacheReadTokens += s.CacheReadTokens
		agg.CacheWriteTokens += s.CacheWriteTokens
		agg.PremiumRequests += s.PremiumRequests
		for model, mu := range s.ModelMetrics {
			existing := agg.ModelMetrics[model]
			existing.InputTokens += mu.InputTokens
			existing.OutputTokens += mu.OutputTokens
			existing.CacheReadTokens += mu.CacheReadTokens
			existing.CacheWriteTokens += mu.CacheWriteTokens
			existing.RequestCount += mu.RequestCount
			existing.RequestCost += mu.RequestCost
			agg.ModelMetrics[model] = existing
		}
	}
	if agg.IsZero() && len(agg.ModelMetrics) == 0 {
		return nil
	}
	if providerConsistent {
		agg.Provider = provider
		agg.ProviderHost = providerHost
	} else {
		agg.Provider = UsageProviderMixed
	}
	return agg
}

type TestStats struct {
	PassRate         float64 `json:"pass_rate"`
	FlakinessPercent float64 `json:"flakiness_percent"`
	PassedRuns       int     `json:"passed_runs"`
	FailedRuns       int     `json:"failed_runs"`
	ErrorRuns        int     `json:"error_runs"`
	TotalRuns        int     `json:"total_runs"`
	AvgScore         float64 `json:"avg_score"`
	AvgWeightedScore float64 `json:"avg_weighted_score"`
	MinScore         float64 `json:"min_score"`
	MaxScore         float64 `json:"max_score"`
	StdDevScore      float64 `json:"std_dev_score"`
	ScoreVariance    float64 `json:"score_variance"`
	CI95Lo           float64 `json:"ci95_lo"`
	CI95Hi           float64 `json:"ci95_hi"`
	Flaky            bool    `json:"flaky"`
	AvgDurationMs    int64   `json:"avg_duration_ms"`

	// Bootstrap confidence interval over weighted scores (populated when trials > 1)
	BootstrapCI   *statistics.ConfidenceInterval `json:"bootstrap_ci,omitempty"`
	IsSignificant *bool                          `json:"is_significant,omitempty"`
}

// StatisticalSummary holds aggregate statistical data for the digest when trials > 1.
type StatisticalSummary struct {
	BootstrapCI    statistics.ConfidenceInterval `json:"bootstrap_ci"`
	IsSignificant  bool                          `json:"is_significant"`
	NormalizedGain *float64                      `json:"normalized_gain,omitempty"`
}

// SkillImpactMetric represents A/B comparison for a single task
type SkillImpactMetric struct {
	PassRateWithSkills float64         `json:"pass_rate_with_skills"`
	PassRateBaseline   float64         `json:"pass_rate_baseline"`
	Delta              float64         `json:"delta"`
	PercentChange      float64         `json:"percent_change"`
	Pairwise           *PairwiseResult `json:"pairwise,omitempty"`
}

// PairwiseResult captures the outcome of a pairwise LLM judge comparison.
type PairwiseResult struct {
	Winner             string `json:"winner"`    // "baseline", "skill", or "tie"
	Magnitude          string `json:"magnitude"` // "much-better", "slightly-better", "equal", etc.
	Reasoning          string `json:"reasoning"`
	PositionConsistent bool   `json:"position_consistent"` // true if result held after position swap
}

// ComputeRunScore calculates the average score across all validations (unweighted, for backward compat)
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

// ComputeWeightedRunScore calculates the weighted composite score (0.0–1.0)
// using each grader's Weight field. If all weights are zero, falls back to simple average.
func (r *RunResult) ComputeWeightedRunScore() float64 {
	if len(r.Validations) == 0 {
		return 0.0
	}
	totalWeight := 0.0
	weightedSum := 0.0
	for _, v := range r.Validations {
		w := v.Weight
		if w <= 0 {
			w = 1.0
		}
		weightedSum += v.Score * w
		totalWeight += w
	}
	if totalWeight == 0 {
		return 0.0
	}
	return weightedSum / totalWeight
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
