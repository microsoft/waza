package validation

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryExamplesConformToEvalAndTaskSchemas(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	for _, evalPath := range []string{
		"examples/scenarios/recovery/mcp/eval.yaml",
		"examples/scenarios/recovery/cli/eval.yaml",
	} {
		t.Run(evalPath, func(t *testing.T) {
			evalErrors, taskErrors, err := ValidateEvalFile(filepath.Join(root, evalPath))
			require.NoError(t, err)
			require.Empty(t, evalErrors)
			require.Empty(t, taskErrors)
		})
	}
}
