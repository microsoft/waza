package runnercontract

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunnerOversizedObservationTerminalizesToBoundedFailure(t *testing.T) {
	service := NewService()
	service.execute = func(context.Context, *admitted) (*models.RunResult, error) {
		run := passed()
		run.FinalOutput = strings.Repeat("x", MaxResultBytes)
		return run, nil
	}
	result := service.Run(t.Context(), wire(t, fixture(t)))
	require.Equal(t, InfrastructureFailure, result.Outcome)
	require.Equal(t, "terminalization", result.Boundary)
	require.Nil(t, result.Observation)
	require.NotNil(t, result.Identity, "terminalization failure must retain validated logical lineage")
	require.NoError(t, ValidateResult(&result))
	var out strings.Builder
	require.NoError(t, Write(&out, &result))
	_, err := ParseResult([]byte(out.String()))
	require.NoError(t, err)
}
