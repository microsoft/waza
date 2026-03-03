package execution

import "github.com/microsoft/waza/internal/models"

// UpdateOutcomeUsage replaces fallback per-turn usage data in the outcome
// with authoritative post-shutdown usage data from the engine, then
// re-aggregates the digest-level usage totals. Call after engine.Shutdown().
func UpdateOutcomeUsage(outcome *models.EvaluationOutcome, engine AgentEngine) {
	if outcome == nil {
		return
	}

	for i := range outcome.TestOutcomes {
		for j := range outcome.TestOutcomes[i].Runs {
			run := &outcome.TestOutcomes[i].Runs[j]
			if run.SessionDigest.SessionID == "" {
				continue
			}
			if usage := engine.SessionUsage(run.SessionDigest.SessionID); usage != nil {
				run.SessionDigest.Usage = usage
			}
		}
	}

	// Re-aggregate usage across all runs
	var allUsage []*models.UsageStats
	for _, to := range outcome.TestOutcomes {
		for _, run := range to.Runs {
			if run.SessionDigest.Usage != nil {
				allUsage = append(allUsage, run.SessionDigest.Usage)
			}
		}
	}
	for _, tr := range outcome.TriggerResults {
		if tr.SessionID != "" {
			if usage := engine.SessionUsage(tr.SessionID); usage != nil {
				allUsage = append(allUsage, usage)
			}
		}
	}
	outcome.Digest.Usage = models.AggregateUsageStats(allUsage)
}
