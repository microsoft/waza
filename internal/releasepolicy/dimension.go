package releasepolicy

func dimensionSeverity(state string) int {
	switch state {
	case "compatible", "complete", "observed", "passed", "within_budget", "not_required":
		return 0
	case "not_assessed", "inconclusive", "unavailable", "incomplete":
		return 1
	case "partial":
		return 2
	case "missing", "missing_required_evidence":
		return 3
	case "failed", "exceeded":
		return 4
	case "mismatched":
		return 5
	case "invalid":
		return 6
	default:
		panic("unregistered release dimension state: " + state)
	}
}

func escalate(d *Dimension, state, reason string) {
	if dimensionSeverity(state) > dimensionSeverity(d.State) {
		d.State = state
	}
	if reason != "" {
		d.Reasons = append(d.Reasons, reason)
	}
}
