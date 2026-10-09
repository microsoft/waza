package reporting

import (
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/evidence"
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
				if run.Evidence != nil {
					if err := evidence.Bind(run.Evidence, outcome.RunID, to.TestID, run.RunNumber, run.Attempts, to.Cached); err != nil {
						fmt.Fprintf(&b, "    Run %d evidence: invalid metadata; assessment references are unavailable.\n", run.RunNumber)
						continue
					}
					fmt.Fprintf(&b, "    Run %d evidence: %s; source eval %s; captured metadata is not verified state or enforcement.\n",
						run.RunNumber, run.Evidence.Version, run.Evidence.Origin.EvalID)
				}
				for _, explanation := range run.RequirementExplanations {
					for _, check := range explanation.Checks {
						fmt.Fprintf(&b, "    Run %d requirement %s: %s/%s = %s (%s). %s\n",
							run.RunNumber, explanation.RequirementID, check.Check.Scope, check.Check.Grader,
							check.Observation, check.Category, check.Message)
					}
				}
			}
		}
	}

	return b.String()
}
