package runnercontract

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerTerminalMetadataFallbackIsSchemaValid(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	result.Identity.CanonicalDigest = strings.Repeat("x", MaxResultBytes)
	result.Diagnostics = []Diagnostic{{Code: "", Message: ""}}
	failed := PublicationFailed(result, errors.New("publication unavailable"))
	require.NoError(t, ValidateResult(&failed))
	require.Equal(t, InfrastructureFailure, failed.Outcome)
	require.Equal(t, "terminalization", failed.Boundary)
	require.Nil(t, failed.Identity)
	require.Nil(t, failed.Resolved)
	require.False(t, failed.SelectionValid)
	var out strings.Builder
	require.NoError(t, Write(&out, &failed))
	_, err := ParseResult([]byte(out.String()))
	require.NoError(t, err)
	require.Contains(t, out.String(), "publication unavailable")
	require.Contains(t, out.String(), "evidence_omitted")
}

func TestRunnerPreselectionTerminalsBoundOversizedDiagnostics(t *testing.T) {
	message := "original cause: " + strings.Repeat("é", MaxResultBytes/2)
	for _, result := range []Result{Rejected(message), Interrupted(errors.New(message))} {
		require.NoError(t, ValidateResult(&result))
		require.Nil(t, result.Identity)
		require.Nil(t, result.Resolved)
		require.Contains(t, result.Diagnostics[0].Message, "original cause:")
		require.Contains(t, result.Diagnostics[0].Message, "[truncated]")
		var out strings.Builder
		require.NoError(t, Write(&out, &result))
		require.Less(t, out.Len(), 16384)
		_, err := ParseResult([]byte(out.String()))
		require.NoError(t, err)
	}
}
