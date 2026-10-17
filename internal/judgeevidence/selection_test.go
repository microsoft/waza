package judgeevidence

import (
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSavedSelectionValidatesUnionWithoutCapturePlatformAdmission(t *testing.T) {
	for _, exclusions := range []bool{false, true} {
		for _, count := range []int{63, 64, 65} {
			t.Run(fmt.Sprintf("exclusions=%t/count=%d", exclusions, count), func(t *testing.T) {
				left, right := &models.PromptEvidenceContext{}, &models.PromptEvidenceContext{}
				for i := range count {
					selected := left
					if i%2 != 0 {
						selected = right
					}
					path := fmt.Sprintf("reports/%d.txt", i)
					if exclusions {
						selected.EvaluatorOnly = append(selected.EvaluatorOnly, path)
					} else {
						selected.Files = append(selected.Files, path)
					}
				}
				configs := []models.GraderConfig{{Identifier: "eval", Parameters: models.PromptGraderParameters{Prompt: "Judge", EvidenceContext: left}}}
				task := &models.TestCase{Validators: []models.ValidatorInline{{
					Identifier: "task", Parameters: models.PromptGraderParameters{Prompt: "Judge", EvidenceContext: right},
				}}}
				err := ValidateSelection(configs, task)
				if count > models.JudgeEvidenceMaxFiles {
					require.ErrorContains(t, err, "union exceeds 64")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}
