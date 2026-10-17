package assurance

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCalibrationRejectsEvidenceContextBeforeRubricOrRuntimeAccess(t *testing.T) {
	_, err := runCalibrationGrade(t.Context(), calibrationJob{config: models.GraderConfig{
		Identifier: "evidence-judge", Kind: models.GraderKindPrompt,
		Parameters: models.PromptGraderParameters{Prompt: "Judge", EvidenceContext: &models.PromptEvidenceContext{}},
	}}, nil, nil)
	require.ErrorContains(t, err, "does not support evidence_context")
}
