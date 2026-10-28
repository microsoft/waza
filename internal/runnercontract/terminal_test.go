package runnercontract

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestRunnerPublicationFailureAtExactEnvelopeLimit(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	result.Observation.FinalOutput = ""
	data, err := json.Marshal(result)
	require.NoError(t, err)
	result.Observation.FinalOutput = strings.Repeat("x", MaxResultBytes-len(data)-1)
	require.NoError(t, ValidateResult(&result))
	data, err = json.Marshal(result)
	require.NoError(t, err)
	require.Len(t, data, MaxResultBytes-1)
	failed := PublicationFailed(result, errors.New(strings.Repeat("delivery failure ", MaxResultBytes/16)))
	require.Equal(t, InfrastructureFailure, failed.Outcome)
	require.Equal(t, "terminalization", failed.Boundary)
	require.NotNil(t, failed.Identity)
	require.Nil(t, failed.Observation)
	require.Equal(t, "publication_failed", failed.Diagnostics[len(failed.Diagnostics)-1].Code)
	require.Contains(t, failed.Diagnostics[len(failed.Diagnostics)-1].Message, "delivery failure")
	require.Contains(t, failed.Diagnostics[0].Message, "sha256=")
	require.NoError(t, ValidateResult(&failed))
	var out strings.Builder
	require.NoError(t, Write(&out, &failed))
	require.Less(t, out.Len(), 32768)
	parsed, err := ParseResult([]byte(out.String()))
	require.NoError(t, err)
	require.Equal(t, failed, *parsed)
	require.ErrorIs(t, Write(terminalBrokenWriter{}, &failed), io.ErrClosedPipe)
}

type terminalBrokenWriter struct{}

func (terminalBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRunnerOversizedExecutionDiagnosticRetainsOriginalFailure(t *testing.T) {
	service := NewService()
	service.execute = func(context.Context, *admitted) (*models.RunResult, error) {
		return nil, &stageError{stage: "shutdown", err: errors.New("shutdown evidence: " + strings.Repeat("x", MaxResultBytes))}
	}
	result := service.Run(t.Context(), wire(t, fixture(t)))
	require.Equal(t, InfrastructureFailure, result.Outcome)
	require.NoError(t, ValidateResult(&result))
	require.Equal(t, "execution_error", result.Diagnostics[0].Code)
	require.Contains(t, result.Diagnostics[0].Message, "shutdown evidence:")
	require.Contains(t, result.Diagnostics[1].Message, "boundary=shutdown")
	result = PublicationFailed(result, errors.New("disk full"))
	require.NoError(t, ValidateResult(&result))
	require.Equal(t, "publication_failed", result.Diagnostics[len(result.Diagnostics)-1].Code)
	require.Contains(t, result.Diagnostics[0].Message, "shutdown evidence:")
}
