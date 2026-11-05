package reporting

import (
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
)

// InterpretScore returns a plain-language label for a numeric score (0–1).
func InterpretScore(score float64) string {
	pct := score * 100
	switch {
	case pct > 90:
		return "Excellent (>90%)"
	case pct >= 70:
		return "Good (70-90%)"
	case pct >= 50:
		return "Needs Work (50-70%)"
	default:
		return "Poor (<50%)"
	}
}

// InterpretPassRate returns a human-readable explanation of a pass rate (0–1).
func InterpretPassRate(rate float64) string {
	pct := rate * 100
	switch {
	case pct >= 100:
		return fmt.Sprintf("All tests passed (%.0f%%)", pct)
	case pct >= 80:
		return fmt.Sprintf("Most tests passed (%.0f%%)", pct)
	case pct >= 50:
		return fmt.Sprintf("About half the tests passed (%.0f%%)", pct)
	default:
		return fmt.Sprintf("Few tests passed (%.0f%%)", pct)
	}
}

// InterpretFlaky explains whether results are flaky and what that means.
func InterpretFlaky(flaky bool, passRate float64) string {
	if !flaky {
		return "Results are consistent across runs."
	}
	pct := passRate * 100
	return fmt.Sprintf("Results are flaky — the same test passes and fails across runs (%.0f%% pass rate). Consider increasing trials or investigating non-determinism.", pct)
}

// FormatSummaryReport produces a full plain-language report from an EvaluationOutcome.
func FormatSummaryReport(outcome *models.EvaluationOutcome) string {
	var b strings.Builder

	d := outcome.Digest
	duration := time.Duration(d.DurationMs) * time.Millisecond

	b.WriteString("=== Interpretation ===\n\n")

	fmt.Fprintf(&b, "Overall Score: %.2f — %s\n", d.AggregateScore, InterpretScore(d.AggregateScore))
	if d.WeightedScore != d.AggregateScore {
		fmt.Fprintf(&b, "Weighted Score: %.2f — %s\n", d.WeightedScore, InterpretScore(d.WeightedScore))
	}
	fmt.Fprintf(&b, "Pass Rate:     %s\n", InterpretPassRate(d.SuccessRate))
	fmt.Fprintf(&b, "Duration:      %v\n", duration)

	if d.TotalTests > 0 {
		fmt.Fprintf(&b, "Tests:         %d passed, %d failed, %d errors out of %d total\n",
			d.Succeeded, d.Failed, d.Errors, d.TotalTests)
	}
	if outcome.RegradeUsage != nil {
		credits := "unavailable"
		if usage := judgeevidence.ProjectRegradeUsage(outcome.RegradeUsage); usage != nil && usage.AICredits != nil {
			credits = fmt.Sprint(*usage.AICredits)
		}
		fmt.Fprintf(&b, "Current regrade AI credits: %s; original execution billing is unchanged.\n", credits)
	}

	// Per-task interpretation
	if len(outcome.TestOutcomes) > 0 {
		b.WriteString("\nPer-Task Interpretation:\n")
		for _, to := range outcome.TestOutcomes {
			icon := "✓"
			if to.Status != models.StatusPassed {
				icon = "✗"
			}
			fmt.Fprintf(&b, "  %s %s: %s\n", icon, to.DisplayName, to.Status)
			if to.Stats != nil {
				fmt.Fprintf(&b, "    Score: %.2f — %s\n", to.Stats.AvgScore, InterpretScore(to.Stats.AvgScore))
				fmt.Fprintf(&b, "    %s\n", InterpretFlaky(to.Stats.Flaky, to.Stats.PassRate))
			}
			for _, run := range to.Runs {
				if run.JudgeEvidence != nil || len(run.PromptJudging) > 0 {
					metadata := judgeevidence.ProjectRun(outcome.RunID, to.TestID, to.Cached, run)
					fmt.Fprintf(&b, "    Run %d attempt %d prompt judging: %s; inspection not observable.\n",
						run.RunNumber, run.Attempts, metadata.Assessment)
					if metadata.BundleSHA256 != "" {
						fmt.Fprintf(&b, "      Frozen bundle SHA-256: %s; integrity metadata, not authenticated history.\n", metadata.BundleSHA256)
					}
					for _, observation := range metadata.Observations {
						credits := "unavailable"
						if observation.Usage != nil && observation.Usage.AICredits != nil {
							credits = fmt.Sprint(*observation.Usage.AICredits)
						}
						fmt.Fprintf(&b, "      Grader %s: %s / %s; submission observed: %t; judge AI credits: %s.\n",
							observation.Grader, observation.Purpose, observation.State, observation.SubmissionObserved, credits)
					}
				}
				if run.Evidence != nil {
					if err := evidence.Bind(run.Evidence, outcome.RunID, to.TestID, run.RunNumber, run.Attempts, to.Cached); err != nil {
						fmt.Fprintf(&b, "    Run %d evidence: invalid metadata; assessment references are unavailable.\n", run.RunNumber)
						continue
					}
					sourceEvalID := run.Evidence.Origin.EvalID
					if sourceEvalID == "" {
						sourceEvalID = "unavailable"
					}
					fmt.Fprintf(&b, "    Run %d evidence: %s; source eval %s; captured metadata is not verified state or enforcement.\n",
						run.RunNumber, run.Evidence.Version, sourceEvalID)
				}
				explanations, pointersWithheld, err := evidence.MetadataExplanations(run.Evidence, to.TestID, run.RequirementExplanations)
				if err != nil {
					fmt.Fprintf(&b, "    Run %d requirement observations: invalid references; assessment references are unavailable.\n", run.RunNumber)
					continue
				}
				if pointersWithheld {
					fmt.Fprintf(&b, "    Run %d artifact-relative value pointers: unverified and withheld; references identify artifact metadata only.\n", run.RunNumber)
				}
				for _, explanation := range explanations {
					if len(explanation.Checks) == 0 {
						fmt.Fprintf(&b, "    Run %d requirement %s: check observations unavailable; requirement satisfaction is unassessed.\n",
							run.RunNumber, explanation.RequirementID)
						continue
					}
					for _, check := range explanation.Checks {
						checkLabel := check.Check.Scope + "/" + check.Check.Grader
						if check.Check.AfterTurn > 0 {
							checkLabel += fmt.Sprintf("/turn %d", check.Check.AfterTurn)
						}
						fmt.Fprintf(&b, "    Run %d requirement %s: %s = %s (%s). %s\n",
							run.RunNumber, explanation.RequirementID, checkLabel,
							check.Observation, check.Category, check.Message)
						for _, reference := range check.References {
							fmt.Fprintf(&b, "      Evidence: %s; artifact metadata only; value pointer unverified; eval %s; task %s; run %d; attempt %d; prior attempts %s\n",
								reference.ArtifactID, reference.Origin.EvalID, reference.Origin.TaskID,
								reference.Origin.RunNumber, reference.Origin.AttemptCount, reference.Origin.PriorAttempts)
						}
					}
				}
			}
		}
	}

	return b.String()
}
