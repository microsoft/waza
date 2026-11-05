package judgeevidence

import (
	"math"
	"regexp"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

type RunMetadata struct {
	RunNumber     int                         `json:"runNumber"`
	Attempts      int                         `json:"attempts"`
	Cached        bool                        `json:"cached"`
	Assessment    string                      `json:"assessment"`
	ExecutionMode string                      `json:"executionMode,omitempty"`
	BundleSHA256  string                      `json:"bundleSHA256,omitempty"`
	Coverage      []CoverageMetadata          `json:"coverage,omitempty"`
	Limits        *models.JudgeEvidenceLimits `json:"limits,omitempty"`
	Observations  []ObservationMetadata       `json:"observations"`
	Message       string                      `json:"message"`
}

type CoverageMetadata struct {
	Section      string `json:"section"`
	Availability string `json:"availability"`
	Completeness string `json:"completeness"`
	Count        *int   `json:"count,omitempty"`
}

type ObservationMetadata struct {
	Grader             string         `json:"grader"`
	State              string         `json:"state"`
	Purpose            string         `json:"purpose"`
	SubmissionObserved bool           `json:"submissionObserved"`
	Inspection         string         `json:"inspection"`
	DeliverySHA256     string         `json:"deliverySHA256,omitempty"`
	FileCount          int            `json:"fileCount"`
	Model              string         `json:"model,omitempty"`
	SessionID          string         `json:"sessionID,omitempty"`
	DurationMs         int64          `json:"durationMs"`
	Usage              *UsageMetadata `json:"usage,omitempty"`
	Diagnostics        []string       `json:"diagnostics,omitempty"`
}

type UsageMetadata struct {
	InputTokens  int      `json:"inputTokens"`
	OutputTokens int      `json:"outputTokens"`
	AICredits    *float64 `json:"aiCredits,omitempty"`
}

var judgeMetadataIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. /-]{0,127}$`)

func judgeLabel(value string) string {
	if !judgeMetadataIdentifier.MatchString(value) || snapshot.DefaultPolicy().RedactString(value) != value {
		return "withheld"
	}
	return value
}

// ProjectRun returns content-free metadata after verifying lineage and digests.
func ProjectRun(evalID, taskID string, cached bool, run models.RunResult) RunMetadata {
	row := RunMetadata{RunNumber: run.RunNumber, Attempts: run.Attempts, Cached: cached,
		Assessment: "invalid", Observations: []ObservationMetadata{},
		Message: "Frozen judge evidence is missing or invalid; content and unbound judging claims are withheld."}
	origin, originErr := snapshot.CaptureOrigin(snapshot.CaptureInput{EvalID: evalID,
		Task: &models.TestCase{TestID: taskID}, Run: &run})
	bundle := run.JudgeEvidence
	if bundle == nil && len(run.PromptJudging) == 0 {
		row.Assessment = "unassessed"
		row.Message = "No frozen judging evidence or judge observations were recorded; historical absence is not success."
		return row
	}
	if cached || originErr != nil || VerifyIntegrity(bundle, origin) != nil {
		return row
	}
	row.Assessment = "recorded"
	row.ExecutionMode, row.BundleSHA256 = bundle.ExecutionMode, bundle.SHA256
	limits := bundle.Limits
	row.Limits = &limits
	row.Message = "Integrity-checked producer evidence, not authenticated history or proof of cognitive inspection."
	usable := Verify(bundle, origin) == nil
	if !usable {
		row.Assessment = "unavailable"
		row.Message = "Required frozen evidence is unavailable, incomplete, redacted, or unsupported; no usable judging context is claimed."
	}
	row.Coverage = []CoverageMetadata{
		{Section: "task", Availability: bundle.Task.Availability, Completeness: bundle.Task.Completeness},
		{Section: "output", Availability: bundle.Output.Availability, Completeness: bundle.Output.Completeness},
		{Section: "messages", Availability: bundle.Messages.Availability, Completeness: bundle.Messages.Completeness},
		{Section: "tool_events", Availability: bundle.ToolEvents.Availability, Completeness: bundle.ToolEvents.Completeness},
		{Section: "files", Availability: bundle.Files.Availability, Completeness: bundle.Files.Completeness},
	}
	if bundle.Messages.Items != nil {
		count := len(*bundle.Messages.Items)
		row.Coverage[2].Count = &count
	}
	if bundle.ToolEvents.Items != nil {
		count := len(*bundle.ToolEvents.Items)
		row.Coverage[3].Count = &count
	}
	if bundle.Files.Items != nil {
		count := len(*bundle.Files.Items)
		row.Coverage[4].Count = &count
	}
	for _, observation := range run.PromptJudging {
		projected := ObservationMetadata{Grader: judgeLabel(observation.Grader), State: "invalid",
			Purpose: "unknown", Inspection: "not_observable", Diagnostics: []string{"invalid_observation"}}
		valid := observation.BundleSHA256 == bundle.SHA256 &&
			observation.Context == "frozen_inline" && observation.Inspection == "not_observable" &&
			(observation.Purpose == "run" || observation.Purpose == "regrade") &&
			(observation.State == "prepared" || observation.State == "submitted" || observation.State == "failed") &&
			observation.DurationMs >= 0
		if observation.State == "submitted" && (!usable || !observation.SubmissionObserved) {
			valid = false
		}
		if valid && observation.DeliverySHA256 != "" {
			delivery, err := Delivery(bundle, origin, observation.EvidenceContext)
			valid = err == nil && delivery.SHA256 == observation.DeliverySHA256
		} else if observation.State != "failed" || observation.SubmissionObserved {
			valid = false
		}
		if valid {
			projected.State, projected.Purpose = observation.State, observation.Purpose
			projected.SubmissionObserved = observation.SubmissionObserved
			projected.DeliverySHA256 = observation.DeliverySHA256
			projected.FileCount = len(observation.EvidenceContext.Files)
			projected.DurationMs = observation.DurationMs
			projected.Diagnostics = nil
			if observation.Model != "" {
				projected.Model = judgeLabel(observation.Model)
			}
			if observation.SessionID != "" {
				projected.SessionID = judgeLabel(observation.SessionID)
			}
			projected.Usage = ProjectUsage(observation.Usage)
			for _, diagnostic := range observation.Diagnostics {
				switch diagnostic.Code {
				case "invalid_evidence", "judge_failed", "judge_policy_denied":
					projected.Diagnostics = append(projected.Diagnostics, diagnostic.Code)
				default:
					projected.Diagnostics = append(projected.Diagnostics, "judge_failed")
				}
			}
		}
		row.Observations = append(row.Observations, projected)
	}
	return row
}

func ProjectUsage(usage *models.UsageStats) *UsageMetadata {
	if usage == nil || usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return nil
	}

	out := &UsageMetadata{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
	if credits := usage.AICredits; credits != nil && *credits >= 0 && !math.IsNaN(*credits) && !math.IsInf(*credits, 0) {
		copy := *credits
		out.AICredits = &copy
	}
	return out
}

func ProjectRegradeUsage(usage *models.EvaluationUsage) *UsageMetadata {
	if usage == nil || len(usage.Sessions) == 0 {
		return nil
	}
	stats := make([]*models.UsageStats, 0, len(usage.Sessions))
	for _, session := range usage.Sessions {
		if session.Usage == nil {
			return nil
		}
		stats = append(stats, execution.EvaluationSessionStats(session))
	}
	return ProjectUsage(models.AggregateUsageStats(stats))
}
