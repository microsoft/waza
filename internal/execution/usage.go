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
			for k := range run.GraderSessions {
				digest := &run.GraderSessions[k]
				if digest.SessionID != "" {
					if usage := engine.SessionUsage(digest.SessionID); usage != nil {
						digest.Usage = usage
					}
				}
			}
			usage := run.SessionDigest.Usage
			if run.SessionDigest.SessionID == "" {
				run.Usage = usage
				continue
			}
			if usage := engine.SessionUsage(run.SessionDigest.SessionID); usage != nil {
				run.SessionDigest.Usage = usage
				run.Usage = usage
				continue
			}
			run.Usage = usage
		}
	}

	if outcome.BaselineOutcome != nil && outcome.BaselineOutcome != outcome {
		UpdateOutcomeUsage(outcome.BaselineOutcome, engine)
	}

	// Re-aggregate usage across all runs
	var allUsage []*models.UsageStats
	seen := make(map[string]bool)
	add := func(digest models.SessionDigest) {
		if digest.SessionID != "" {
			if seen[digest.SessionID] {
				return
			}
			seen[digest.SessionID] = true
		}
		allUsage = append(allUsage, digest.Usage)
	}
	for _, to := range outcome.TestOutcomes {
		for _, run := range to.Runs {
			add(run.SessionDigest)
			for _, digest := range run.GraderSessions {
				add(digest)
			}
		}
	}
	for _, tr := range outcome.TriggerResults {
		if tr.SessionID != "" {
			add(models.SessionDigest{SessionID: tr.SessionID, Usage: engine.SessionUsage(tr.SessionID)})
		}
	}
	outcome.Digest.Usage = models.AggregateUsageStats(allUsage)
}
