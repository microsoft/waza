package releasepolicy

import (
	"fmt"
	"math"

	"github.com/microsoft/waza/internal/models"
)

// VerifyRunRow checks independently recorded actual results, not a second
// independently hash-valid summary. Raw prompt/output/details are deliberately
// outside the decision projection; check verdicts and allocated keys are not.
func VerifyRunRow(summary AttemptSummary, actual ActualRunRow) error {
	run := actual.Run
	if actual.Origin != summary.Origin || run.RunNumber != summary.Key.Trial || run.Attempts != summary.Key.Attempt {
		return fmt.Errorf("actual result origin/run/attempt differs from its allocated key")
	}
	if summary.Category == "behavioral" {
		if run.ErrorMsg != "" || string(run.Status) != summary.Status ||
			(run.Status != models.StatusPassed && run.Status != models.StatusFailed) {
			return fmt.Errorf("actual operational status cannot be classified as behavior")
		}
	}
	if summary.Category == "operational" && run.Status != models.StatusError && run.Status != models.StatusSkipped {
		return fmt.Errorf("operational category lacks an actual error/skip status")
	}
	if len(run.Validations) != len(summary.Checks) {
		return fmt.Errorf("actual grader inventory differs from scoped check summaries")
	}
	seen := map[string]bool{}
	for _, check := range summary.Checks {
		result, found := run.Validations[check.Grader]
		score, err := check.Score.Float64()
		if !found || seen[check.Grader] || result.Name != check.Grader ||
			err != nil || math.IsNaN(result.Score) || math.IsInf(result.Score, 0) ||
			result.Passed != check.Passed || result.Score != score {
			return fmt.Errorf("actual grader verdict/score contradicts summary: %s", check.Grader)
		}
		seen[check.Grader] = true
	}
	return nil
}

func VerifyResults(receipt Receipt, results Results) error {
	if results.CollectionID != receipt.CollectionID || results.Arm != receipt.Arm ||
		results.EvalID != receipt.EvalID || len(results.Rows) != len(receipt.Attempts) {
		return fmt.Errorf("actual result collection/arm/eval/inventory differs from receipt")
	}
	for i, summary := range receipt.Attempts {
		if err := VerifyRunRow(summary, results.Rows[i]); err != nil {
			return fmt.Errorf("actual result row %d: %w", i+1, err)
		}
	}
	return nil
}
