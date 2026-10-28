//go:build linux || darwin

package webapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

type oversizedRunnerEngine struct{}

func (*oversizedRunnerEngine) Initialize(context.Context) error       { return nil }
func (*oversizedRunnerEngine) Shutdown(context.Context) error         { return nil }
func (*oversizedRunnerEngine) SessionUsage(string) *models.UsageStats { return nil }
func (*oversizedRunnerEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return &execution.ExecutionResponse{
		Success: true, FinalOutput: "hello" + strings.Repeat("x", runnercontract.MaxResultBytes),
	}, nil
}

func TestRunnerHTTPBothOversizedEvidenceAndPersistenceFailureRemainValid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(dir, []byte("unchanged"), 0o600))
	service := runnercontract.NewService(runnercontract.WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
		return &oversizedRunnerEngine{}, nil
	}))
	mux := http.NewServeMux()
	registerRunnerRoutes(mux, dir, service)
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Less(t, response.Body.Len(), 16384)
	result, err := runnercontract.ParseResult(response.Body.Bytes())
	require.NoError(t, err)
	require.NoError(t, runnercontract.ValidateResult(result))
	require.Equal(t, runnercontract.InfrastructureFailure, result.Outcome)
	require.Equal(t, "terminalization", result.Boundary)
	require.NotNil(t, result.Identity)
	require.Nil(t, result.Observation)
	codes := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	require.Contains(t, codes, "observation_omitted")
	require.Contains(t, codes, "invalid_envelope")
	require.Equal(t, "publication_failed", codes[len(codes)-1])
	unchanged, err := os.ReadFile(dir)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(unchanged))
}
