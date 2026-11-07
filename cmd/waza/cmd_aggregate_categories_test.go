package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func aggregateOwnedCapture(t *testing.T, directory string, usage *models.UsageStats) evidencestate.Inspection {
	t.Helper()
	storePath := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storePath, 0o700))
	backend, closeBackend, err := artifactmanifest.OpenBackend(storePath, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeBackend()) })
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admission := runnerunit.Validate(data)
	require.Equal(t, "ready", admission.Status)
	require.Zero(t, admission.Calls)
	_, admittedBinding := admission.Terminal()
	require.NotNil(t, admittedBinding)
	require.NotNil(t, admittedBinding.Identity)
	binding := artifactmanifest.Binding{
		Identity: *admittedBinding.Identity, Selection: admittedBinding.Selection,
		ExecutionMode: admittedBinding.ExecutionMode, GraderIDs: admittedBinding.GraderIDs,
	}
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	creator, err := ledger.Reserve(t.Context(), binding, nil)
	require.NoError(t, err)
	factories := 0
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories++
		require.NoError(t, ledger.ValidateCreator(t.Context(), creator), "reservation precedes the actual factory")
		return execution.NewMockEngine(config.ModelID), nil
	})
	report := service.Run(t.Context(), data)
	require.Equal(t, 1, factories)
	require.Equal(t, 1, report.Calls)
	require.Equal(t, "completed", report.Status)
	require.NotNil(t, report.Observation)
	// Authored precision/presence controls on a real mock report, not observed
	// SDK billing. The actual capture producer still verifies native shape.
	report.Observation.Usage = usage
	if usage != nil {
		require.NotNil(t, report.Observation.Usage)
		require.Equal(t, usage.InputTokens, report.Observation.Usage.InputTokens)
	}
	capture, err := evidencestate.CaptureReport(report, nil)
	require.NoError(t, err)
	published, err := evidencestate.PersistOwnedCapture(t.Context(), backend, capture, ledger, creator)
	require.NoError(t, err)
	require.Equal(t, "verified", published.Integrity)
	require.NotNil(t, published.Document)
	require.NotNil(t, published.Document.ExecutionSource)
	completed, err := ledger.ReadCompleted(t.Context(), *published.Document.ExecutionSource)
	require.NoError(t, err)
	require.Equal(t, binding.Identity, completed.Reservation.Attempt.Binding.Identity)
	require.Equal(t, 1, factories, "persistence/current-source verification do not execute a worker")
	return published
}

func TestAggregateOwnedProducerCLIAPIExactIntegerCSV(t *testing.T) {
	for _, integer := range []string{"9007199254740993", "9223372036854775807"} {
		for _, credits := range []*float64{nil, new(float64)} {
			t.Run(integer+"/"+strconv.FormatBool(credits != nil), func(t *testing.T) {
				directory := t.TempDir()
				tokens, err := strconv.Atoi(integer)
				require.NoError(t, err)
				published := aggregateOwnedCapture(t, directory, &models.UsageStats{
					InputTokens: tokens, AICredits: credits,
					ModelMetrics: map[string]models.ModelUsage{"recorded-mock": {InputTokens: tokens, AICredits: credits}},
				})
				origin := published.Document.OriginManifest
				request := manifestaggregate.Request{Manifests: []string{origin}}
				pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: origin, EvidenceSHA256: published.EvidenceSHA256}}
				request.Evidence = &pairs
				body, err := json.Marshal(request)
				require.NoError(t, err)
				mux := http.NewServeMux()
				webapi.RegisterAggregateRoutes(mux, directory, nil)
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(body)))
				require.Equal(t, http.StatusOK, response.Code)
				require.Contains(t, response.Body.String(), `"input_tokens":`+integer)
				var aggregate manifestaggregate.Report
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &aggregate))
				require.Len(t, aggregate.Evidence.Members[0].Candidates[0].Categories, 6)
				require.Equal(t, 1, aggregate.Evidence.CategoryStateCounts[evidencestate.Grader][evidencestate.Present])
				require.Equal(t, "withheld", aggregate.Evidence.UsageTotals)
				command := newAggregateCommand()
				var output bytes.Buffer
				command.SetOut(&output)
				command.SetArgs([]string{"--store", filepath.Join(directory, ".waza-artifacts"), "--evidence", origin + "=" + published.EvidenceSHA256, origin})
				require.NoError(t, command.Execute())
				require.Equal(t, response.Body.String(), strings.TrimSuffix(output.String(), "\n"))
				csvResponse := httptest.NewRecorder()
				mux.ServeHTTP(csvResponse, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates?format=csv", bytes.NewReader(body)))
				require.Equal(t, http.StatusOK, csvResponse.Code)
				require.Equal(t, "text/csv; charset=utf-8", csvResponse.Header().Get("Content-Type"))
				rows, err := csv.NewReader(csvResponse.Body).ReadAll()
				require.NoError(t, err)
				require.Len(t, rows, 2)
				require.Equal(t, integer, rows[1][9])
				require.Contains(t, rows[1][15], `"input_tokens":`+integer)
				require.Contains(t, rows[1][18], origin)
				require.Contains(t, rows[1][19], `"verification":"source-content-and-recorded-category-shape-only"`)
				require.Contains(t, rows[1][20], `"declared_state":"present"`)
				require.Equal(t, "withheld", rows[1][16])
				require.Equal(t, "unavailable", rows[1][17])
				if credits == nil {
					require.Empty(t, rows[1][14])
				} else {
					require.Equal(t, "0", rows[1][14])
				}
			})
		}
	}
}

func TestAggregateOwnedCategoriesAbsentUsageAndDeclaredCountConflict(t *testing.T) {
	directory := t.TempDir()
	published := aggregateOwnedCapture(t, directory, nil)
	backend, closeBackend, err := artifactmanifest.OpenBackend(filepath.Join(directory, ".waza-artifacts"), "")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeBackend()) }()
	origin := published.Document.OriginManifest
	pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: origin, EvidenceSHA256: published.EvidenceSHA256}}
	report, err := manifestaggregate.Aggregate(t.Context(), backend, manifestaggregate.Request{Manifests: []string{origin}, Evidence: &pairs})
	require.NoError(t, err)
	require.NotNil(t, report.Evidence)
	candidate := report.Evidence.Members[0].Candidates[0]
	require.Equal(t, evidencestate.Unavailable, candidate.Usage.State)
	require.Nil(t, candidate.Usage.Recorded)
	require.Equal(t, 1, report.Evidence.CategoryStateCounts[evidencestate.Usage][evidencestate.Unavailable])
	_, err = manifestaggregate.Encode(report)
	require.NoError(t, err)

	document := *published.Document
	document.Categories = append([]evidencestate.Record(nil), document.Categories...)
	for index := range document.Categories {
		if document.Categories[index].Category == evidencestate.Grader {
			require.NotNil(t, document.Categories[index].ObservedCount)
			count := *document.Categories[index].ObservedCount + 1
			document.Categories[index].ObservedCount = &count
		}
	}
	conflicting, err := evidencestate.Publish(t.Context(), backend, document)
	require.NoError(t, err, "declared-context publication does not qualify native category counts")
	pairs[0].EvidenceSHA256 = conflicting.EvidenceSHA256
	report, err = manifestaggregate.Aggregate(t.Context(), backend, manifestaggregate.Request{Manifests: []string{origin}, Evidence: &pairs})
	require.ErrorIs(t, err, manifestaggregate.ErrIncomplete)
	require.Equal(t, "incomplete", report.Completion)
	candidate = report.Evidence.Members[0].Candidates[0]
	require.Equal(t, "verified", candidate.State, "byte receipt remains distinct from semantic admission")
	require.Empty(t, report.Evidence.CategoryStateCounts)
	require.Nil(t, candidate.Usage.Recorded)
	require.NotEmpty(t, candidate.Diagnostics)
	_, err = manifestaggregate.Encode(report)
	require.NoError(t, err)
}
