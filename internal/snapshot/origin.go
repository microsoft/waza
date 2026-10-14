package snapshot

import (
	"errors"

	"github.com/microsoft/waza/internal/models"
)

// CaptureOrigin returns policy-validated identity or numeric-only lineage with
// an error. It does not inspect execution content or mutate the caller's policy.
func CaptureOrigin(in CaptureInput) (models.EvidenceOrigin, error) {
	fallback := captureOrigin("", "", in.Run)
	if in.Run == nil {
		return fallback, errors.New("snapshot: capture origin requires Run")
	}
	if in.Task == nil {
		return fallback, errors.New("snapshot: capture origin requires Task")
	}
	if in.EvalID == "" {
		return fallback, errors.New("snapshot: capture origin requires a nonempty eval ID")
	}
	if in.Task.TestID == "" {
		return fallback, errors.New("snapshot: capture origin requires a nonempty task ID")
	}
	policy, err := in.Policy.forCapture()
	if err != nil {
		return fallback, err
	}
	for _, identity := range []struct{ value, field string }{
		{in.EvalID, "eval ID"},
		{in.Task.TestID, "task ID"},
	} {
		if err := policy.checkIdentity(identity.value, identity.field); err != nil {
			return fallback, err
		}
	}
	return captureOrigin(in.EvalID, in.Task.TestID, in.Run), nil
}

func captureOrigin(evalID, taskID string, run *models.RunResult) models.EvidenceOrigin {
	origin := models.EvidenceOrigin{EvalID: evalID, TaskID: taskID, PriorAttempts: "unknown"}
	if run != nil {
		origin.RunNumber = run.RunNumber
		origin.AttemptCount = run.Attempts
		switch run.Attempts {
		case 0:
		case 1:
			origin.PriorAttempts = "none"
		default:
			origin.PriorAttempts = "not_preserved"
		}
	}
	return origin
}
