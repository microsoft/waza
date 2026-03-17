package reporting

import (
	"fmt"
	"strings"
	"time"

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

// FormatSummaryReport produces a full plain-language report from an EvalOutcome.
func FormatSummaryReport(outcome *models.EvalOutcome) string {
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
	if len(outcome.TaskOutcomes) > 0 {
		b.WriteString("\nPer-Task Interpretation:\n")
		for _, to := range outcome.TaskOutcomes {
			icon := "✓"
			if to.Status != models.StatusPassed {
				icon = "✗"
			}
			fmt.Fprintf(&b, "  %s %s: %s\n", icon, to.DisplayName, to.Status)
			if to.Stats != nil {
				fmt.Fprintf(&b, "    Score: %.2f — %s\n", to.Stats.AvgScore, InterpretScore(to.Stats.AvgScore))
				fmt.Fprintf(&b, "    %s\n", InterpretFlaky(to.Stats.Flaky, to.Stats.PassRate))
			}
		}
	}

	return b.String()
}
