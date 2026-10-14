package webapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/storage"
	"github.com/stretchr/testify/require"
)

func labRefreshMux(t *testing.T, dir string, configured bool) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if configured {
		cfg := &projectconfig.StorageConfig{Enabled: true}
		backend, err := storage.NewStore(cfg, dir)
		require.NoError(t, err)
		require.IsType(t, &storage.LocalStore{}, backend)
		RegisterRoutesWithStorage(mux, NewStorageAdapter(backend, "local"), &StorageConfig{Configured: true})
	} else {
		RegisterRoutes(mux, NewFileStore(dir))
	}
	return mux
}

func labRefreshRequest(t *testing.T, mux *http.ServeMux, path string, status int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, status, rec.Code, rec.Body.String())
	return rec
}

func labRefreshRuns(t *testing.T, mux *http.ServeMux, path string) []RunSummary {
	t.Helper()
	rec := labRefreshRequest(t, mux, path, http.StatusOK)
	var runs []RunSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &runs))
	return runs
}

func labRefreshOutcome(id string, score float64) models.EvaluationOutcome {
	return models.EvaluationOutcome{
		RunID: id, BenchName: "refresh-eval", SkillTested: "refresh-skill",
		Timestamp: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		Digest:    models.OutcomeDigest{TotalTests: 1, WeightedScore: score},
		TestOutcomes: []models.TestOutcome{{
			TestID: "stable-task", Stats: &models.TestStats{AvgWeightedScore: score},
			Runs: []models.RunResult{{Prompt: "Recorded original prompt"}},
		}},
		BaselineOutcome: &models.EvaluationOutcome{SchemaVersion: "1.4"},
	}
}

func TestLabCachedArtifactRefreshTransaction(t *testing.T) {
	corruptions := []struct {
		name, contents string
		directory      bool
	}{
		{name: "truncated", contents: "{"},
		{name: "no-result-shape", contents: "{}"},
		{name: "invalid-probe-version", contents: `{"eval_id":"cached","schemaVersion":1}`},
		{name: "invalid-decoded-score", contents: `{"eval_id":"cached","tasks":[{"stats":{"avg_weighted_score":"unfinished"}}]}`},
		{name: "unsupported-nested-version", contents: `{"eval_id":"cached","baseline_outcome":{"schemaVersion":"2.0"}}`},
		{name: "replaced-with-directory", directory: true},
	}
	for _, configured := range []bool{false, true} {
		backend := "file-store"
		if configured {
			backend = "configured-local-adapter"
		}
		for _, fallbackID := range []bool{false, true} {
			identity := "recorded-id"
			if fallbackID {
				identity = "path-id"
			}
			for _, corruption := range corruptions {
				t.Run(backend+"/"+identity+"/"+corruption.name, func(t *testing.T) {
					dir := t.TempDir()
					outcome := labRefreshOutcome("cached", 0.75)
					if fallbackID {
						outcome.RunID = ""
					}
					file := filepath.Join(dir, "cached.json")
					writeOutcomeFile(t, file, outcome)
					mux := labRefreshMux(t, dir, configured)
					require.Len(t, labRefreshRuns(t, mux, "/api/runs"), 1)
					before := labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK).Body.String()
					summary := labRefreshRequest(t, mux, "/api/summary", http.StatusOK).Body.String()
					writeOutcomeFile(t, filepath.Join(dir, "new.json"), labRefreshOutcome("new", 0.5))
					if corruption.directory {
						require.NoError(t, os.Remove(file))
						require.NoError(t, os.Mkdir(file, 0o700))
					} else {
						require.NoError(t, os.WriteFile(file, []byte(corruption.contents), 0o600))
					}
					failed := labRefreshRequest(t, mux, "/api/v1/lab/runs", http.StatusInternalServerError)
					require.Contains(t, failed.Body.String(), file)
					require.JSONEq(t, before, labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK).Body.String())
					require.JSONEq(t, summary, labRefreshRequest(t, mux, "/api/summary", http.StatusOK).Body.String())
					require.Len(t, labRefreshRuns(t, mux, "/api/runs"), 1)
					labRefreshRequest(t, mux, "/api/runs/new", http.StatusNotFound)
					if corruption.directory {
						require.NoError(t, os.Remove(file))
					}
					writeOutcomeFile(t, file, outcome)
					require.NoError(t, os.WriteFile(filepath.Join(dir, "malformed-new.json"), []byte("{"), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte(`{"project":"settings"}`), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid-new-result.json"), []byte(`{"eval_id":"bad","tasks":[{"stats":{"avg_weighted_score":"unfinished"}}]}`), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "unsupported-new-result.json"), []byte(`{"eval_id":"bad","baseline_outcome":{"schemaVersion":"2.0"}}`), 0o600))
					require.Len(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"), 2)
					require.JSONEq(t, before, labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK).Body.String())
					require.NoError(t, os.Remove(file))
					require.NoError(t, os.Remove(filepath.Join(dir, "new.json")))
					require.Empty(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"))
					labRefreshRequest(t, mux, "/api/runs/cached", http.StatusNotFound)
					require.NoError(t, os.WriteFile(file, []byte("{"), 0o600))
					require.Empty(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"), "intentional deletion removes prior path admission")
				})
			}

		}
	}
}

type labRemoteSnapshotStore struct {
	storage.ResultStore
	lists, downloads int
}

func (s *labRemoteSnapshotStore) List(context.Context, storage.ListOptions) ([]storage.ResultSummary, error) {
	s.lists++
	return []storage.ResultSummary{{RunID: "remote"}}, nil
}

func (s *labRemoteSnapshotStore) Download(_ context.Context, id string) (*models.EvaluationOutcome, error) {
	s.downloads++
	outcome := labRefreshOutcome(id, 0.75)
	return &outcome, nil
}

func TestLabRemoteAdapterRefreshKeepsProviderSemantics(t *testing.T) {
	backend := &labRemoteSnapshotStore{}
	mux := http.NewServeMux()
	RegisterRoutesWithStorage(mux, NewStorageAdapter(backend, "azure-blob"), &StorageConfig{Configured: true, Provider: "azure-blob"})
	for i, path := range []string{"/api/runs", "/api/v1/lab/runs"} {
		list := labRefreshRuns(t, mux, path)
		require.Len(t, list, 1)
		require.Equal(t, "azure-blob", list[0].Source)
		require.Equal(t, i+1, backend.lists, "refresh does not add a remote listing")
		require.Equal(t, i+1, backend.downloads, "refresh does not add a remote download")
	}
}

func TestLabConfiguredLocalAdapterRefreshesArtifacts(t *testing.T) {
	for _, change := range []string{"new-file", "same-id-regrade", "intentional-deletion"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			outcome := labRefreshOutcome("cached", 0.75)
			file := filepath.Join(dir, "cached.json")
			writeOutcomeFile(t, file, outcome)
			mux := labRefreshMux(t, dir, true)
			require.Len(t, labRefreshRuns(t, mux, "/api/runs"), 1)
			before := labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK).Body.String()
			switch change {
			case "new-file":
				writeOutcomeFile(t, filepath.Join(dir, "new.json"), labRefreshOutcome("new", 0.25))
				require.Len(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"), 2)
				labRefreshRequest(t, mux, "/api/runs/new", http.StatusOK)
			case "same-id-regrade":
				outcome.Digest.WeightedScore = 0.25
				outcome.TestOutcomes[0].Stats.AvgWeightedScore = 0.25
				outcome.TestOutcomes[0].Runs[0].Prompt = "Regraded recorded prompt"
				writeOutcomeFile(t, file, outcome)
				// Classic readers remain cached until the lab requests refresh.
				require.JSONEq(t, before, labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK).Body.String())
				list := labRefreshRuns(t, mux, "/api/v1/lab/runs")
				require.Len(t, list, 1)
				require.Equal(t, outcome.Timestamp, list[0].Timestamp)
				require.NotNil(t, list[0].WeightedScore)
				require.Equal(t, 0.25, *list[0].WeightedScore)
				rec := labRefreshRequest(t, mux, "/api/runs/cached", http.StatusOK)
				var detail RunDetail
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
				require.Equal(t, "cached", detail.ID)
				require.Equal(t, "local", detail.Source)
				require.Equal(t, 0.25, *detail.Tasks[0].WeightedScore)
				require.Equal(t, "Regraded recorded prompt", detail.Tasks[0].Prompt)
			case "intentional-deletion":
				require.NoError(t, os.Remove(file))
				require.Empty(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"))
				labRefreshRequest(t, mux, "/api/runs/cached", http.StatusNotFound)
			}
		})
	}
}

func TestLabMissingEmptyArchiveKeepsRefreshing(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "file-store", true: "configured-local-adapter"}[configured], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			mux := labRefreshMux(t, dir, configured)
			for range 3 {
				require.Empty(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"))
			}
			writeOutcomeFile(t, filepath.Join(dir, "known.json"), labRefreshOutcome("known", 0.5))
			require.Len(t, labRefreshRuns(t, mux, "/api/v1/lab/runs"), 1)
			require.NoError(t, os.Remove(filepath.Join(dir, "known.json")))
			require.NoError(t, os.Remove(dir))
			labRefreshRequest(t, mux, "/api/v1/lab/runs", http.StatusInternalServerError)
			labRefreshRequest(t, mux, "/api/runs/known", http.StatusOK)
		})
	}
}
