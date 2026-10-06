package webapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestLabOutcomeMetadata(t *testing.T) {
	for _, score := range []float64{0, 0.75} {
		outcome := models.EvaluationOutcome{
			RunID: "metadata", SkillTested: "custom-skill", BenchName: "custom-eval",
			Setup:        models.OutcomeSetup{ModelID: "custom-model", RunsPerTest: 3},
			Digest:       models.OutcomeDigest{TotalTests: 1, WeightedScore: score},
			TestOutcomes: []models.TestOutcome{{TestID: "stable-id", DisplayName: "Display name", Stats: &models.TestStats{AvgWeightedScore: score}}},
		}
		detail := outcomeToDetail(&outcome)
		require.Equal(t, "custom-skill", detail.Skill)
		require.Equal(t, 3, detail.Repetitions)
		require.Equal(t, score, *detail.WeightedScore)
		require.Equal(t, "stable-id", detail.Tasks[0].ID)
		require.Equal(t, score, *detail.Tasks[0].WeightedScore)
		body, err := json.Marshal(detail)
		require.NoError(t, err)
		require.Contains(t, string(body), `"weightedScore":`)
		outcome.Digest.TotalTests = 2
		require.Nil(t, outcomeToSummary(&outcome).WeightedScore, "incomplete task coverage has no complete quality score")
		outcome.Digest.TotalTests = 1
		outcome.TestOutcomes[0].Stats = nil
		missing := outcomeToDetail(&outcome)
		require.Nil(t, missing.WeightedScore)
		require.Nil(t, missing.Tasks[0].WeightedScore)
		body, err = json.Marshal(missing)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"weightedScore":`)
		outcome.TestOutcomes = nil
		require.Nil(t, outcomeToSummary(&outcome).WeightedScore)
	}
}

func TestLabListRefreshesLocalArtifacts(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store)
	request := func(path string) []RunSummary {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var runs []RunSummary
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &runs))
		return runs
	}
	require.Empty(t, request("/api/runs"))
	writeOutcomeFile(t, filepath.Join(dir, "new.json"), models.EvaluationOutcome{RunID: "new", BenchName: "new-eval"})
	require.Empty(t, request("/api/runs"), "classic cached listing stays unchanged")
	require.Len(t, request("/api/v1/lab/runs"), 1)
	require.Equal(t, "new", request("/api/v1/lab/runs")[0].ID)
}

func TestLabListFailures(t *testing.T) {
	store := newMockStore()
	store.listErr = errors.New("list unavailable")
	for _, storageRoutes := range []bool{false, true} {
		mux := http.NewServeMux()
		if storageRoutes {
			RegisterRoutesWithStorage(mux, store, nil)
		} else {
			RegisterRoutes(mux, store)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runs", nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "list unavailable")
	}
	badStore := NewFileStore(strings.Repeat("x", 1024))
	rec := httptest.NewRecorder()
	NewHandlers(badStore).HandleLabRuns(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runs", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "refreshing evaluation results")
}

func TestLabDetailPreservesStorageSource(t *testing.T) {
	store := storage.NewLocalStore(t.TempDir())
	outcome := models.EvaluationOutcome{RunID: "source", BenchName: "source-eval"}
	require.NoError(t, store.Upload(t.Context(), &outcome))
	detail, err := NewStorageAdapter(store, "azure-blob").GetRun("source")
	require.NoError(t, err)
	require.Equal(t, "azure-blob", detail.Source)
}
