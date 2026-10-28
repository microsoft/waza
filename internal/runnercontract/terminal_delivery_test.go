package runnercontract

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerPublicationFailureBoundsDiagnosticsBeforeDiscardingObservation(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	residue := strings.Repeat("deep/", 700) + "result.runner.json"
	failed := PublicationFailed(result, errors.New("publication evidence: residue="+residue+"; "+strings.Repeat("x", MaxResultBytes)))
	require.NoError(t, ValidateResult(&failed))
	require.Equal(t, InfrastructureFailure, failed.Outcome)
	require.Equal(t, result.Observation, failed.Observation)
	require.Equal(t, result.Identity, failed.Identity)
	require.Contains(t, failed.Diagnostics[len(failed.Diagnostics)-1].Message, "publication evidence:")
	require.Contains(t, failed.Diagnostics[len(failed.Diagnostics)-1].Message, residue)
	require.Contains(t, failed.Diagnostics[len(failed.Diagnostics)-1].Message, "[truncated]")
}
