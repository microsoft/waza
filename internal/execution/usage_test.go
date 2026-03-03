package execution

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestUpdateOutcomeUsage_NilOutcome(t *testing.T) {
	// Should not panic
	UpdateOutcomeUsage(nil, NewMockEngine("test"))
}

func TestUpdateOutcomeUsage_ReplacesAndReaggregates(t *testing.T) {
	// Create an outcome with fallback usage
	outcome := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{
				Runs: []models.RunResult{
					{
						SessionDigest: models.SessionDigest{
							SessionID: "session-1",
							Usage: &models.UsageStats{
								InputTokens:  100,
								OutputTokens: 50,
							},
						},
					},
				},
			},
		},
		Digest: models.OutcomeDigest{
			Usage: &models.UsageStats{
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
	}

	// Mock engine returns nil for SessionUsage (no post-shutdown data)
	engine := NewMockEngine("test")
	UpdateOutcomeUsage(outcome, engine)

	// Usage should be unchanged (mock returns nil)
	require.Equal(t, 100, outcome.TestOutcomes[0].Runs[0].SessionDigest.Usage.InputTokens)
}

func TestUpdateOutcomeUsage_SkipsEmptySessionID(t *testing.T) {
	originalUsage := &models.UsageStats{InputTokens: 100}
	outcome := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{
				Runs: []models.RunResult{
					{
						SessionDigest: models.SessionDigest{
							SessionID: "", // empty — should be skipped
							Usage:     originalUsage,
						},
					},
				},
			},
		},
		Digest: models.OutcomeDigest{},
	}

	engine := NewMockEngine("test")
	UpdateOutcomeUsage(outcome, engine)

	// Usage unchanged
	require.Equal(t, originalUsage, outcome.TestOutcomes[0].Runs[0].SessionDigest.Usage)
}
