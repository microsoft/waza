package webapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestLabFailedRefreshPreservesCachedEvidence(t *testing.T) {
	dir := t.TempDir()
	outcome := models.EvaluationOutcome{
		RunID: "cached", BenchName: "cached-eval", SkillTested: "cached-skill",
		Digest: models.OutcomeDigest{TotalTests: 1, WeightedScore: 0.75},
		TestOutcomes: []models.TestOutcome{{
			TestID: "task", Stats: &models.TestStats{AvgWeightedScore: 0.75},
			Runs: []models.RunResult{{Prompt: "Recorded prompt", Transcript: []models.TranscriptEvent{{
				SessionEvent: copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "Recorded evidence"}},
			}}}},
		}},
	}
	writeOutcomeFile(t, filepath.Join(dir, "cached.json"), outcome)
	store := NewFileStore(dir)
	before, err := store.GetRun("cached")
	require.NoError(t, err)
	summary, err := store.Summary()
	require.NoError(t, err)
	// A path component that is a file causes ENOTDIR rather than an empty archive.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("blocked"), 0o600))
	for name, path := range map[string]string{
		"invalid component": filepath.Join(blocker, "results"),
		"not a directory":   blocker,
		"missing directory": filepath.Join(t.TempDir(), "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			store.dir = path
			rec := httptest.NewRecorder()
			NewHandlers(store).HandleLabRuns(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runs", nil))
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			after, err := store.GetRun("cached")
			require.NoError(t, err)
			require.Equal(t, before, after)
			afterSummary, err := store.Summary()
			require.NoError(t, err)
			require.Equal(t, summary, afterSummary)
			list, err := store.ListRuns("", "")
			require.NoError(t, err)
			require.Len(t, list, 1)
			require.Equal(t, "local", after.Source)
			require.Equal(t, "Recorded prompt", after.Tasks[0].Prompt)
			require.Equal(t, "Recorded evidence", after.Tasks[0].Transcript[0].Content)
		})
	}
	store.dir = dir
	require.NoError(t, store.Reload(), "a corrected directory can recover after refresh failure")
	recovered, err := store.GetRun("cached")
	require.NoError(t, err)
	require.Equal(t, before, recovered)
	require.NoError(t, os.Remove(filepath.Join(dir, "cached.json")))
	require.NoError(t, store.Reload(), "a successfully scanned empty archive replaces the cache")
	list, err := store.ListRuns("", "")
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestLabRecordedWeightedScorePresence(t *testing.T) {
	for _, taskScore := range []string{"", `,"avg_weighted_score":null`, `,"avg_weighted_score":0`, `,"avg_weighted_score":0.75`} {
		for _, suiteScore := range []string{"", `,"weighted_score":null`, `,"weighted_score":0`, `,"weighted_score":0.75`} {
			t.Run(taskScore+"/"+suiteScore, func(t *testing.T) {
				data := `{"eval_id":"presence","eval_name":"presence","summary":{"total_tests":1` + suiteScore + `},"tasks":[{"test_id":"task","stats":{"avg_score":0.8` + taskScore + `}}]}`
				outcome, err := models.ParseEvaluationOutcome([]byte(data), "presence.json")
				require.NoError(t, err)
				detail := outcomeToDetail(outcome)
				taskPresent := taskScore != "" && !strings.Contains(taskScore, "null")
				suitePresent := suiteScore != "" && !strings.Contains(suiteScore, "null")
				require.Equal(t, taskPresent, detail.Tasks[0].WeightedScore != nil)
				require.Equal(t, suitePresent, detail.WeightedScore != nil)
				if taskPresent {
					want := 0.0
					if strings.Contains(taskScore, "0.75") {
						want = 0.75
					}
					require.Equal(t, want, *detail.Tasks[0].WeightedScore)
				}
				if suitePresent {
					want := 0.0
					if strings.Contains(suiteScore, "0.75") {
						want = 0.75
					}
					require.Equal(t, want, *detail.WeightedScore)
				}
				body, err := json.Marshal(detail)
				require.NoError(t, err)
				if !taskPresent && !suitePresent {
					require.NotContains(t, string(body), `"weightedScore":`)
				}
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "presence.json"), []byte(data), 0o600))
				fromFile, err := NewFileStore(dir).GetRun("presence")
				require.NoError(t, err)
				require.Equal(t, detail, fromFile)
				storageStore := storage.NewLocalStore(t.TempDir())
				require.NoError(t, storageStore.Upload(t.Context(), outcome))
				fromStorage, err := NewStorageAdapter(storageStore, "azure-blob").GetRun("presence")
				require.NoError(t, err)
				require.Equal(t, taskPresent, fromStorage.Tasks[0].WeightedScore != nil)
				require.Equal(t, suitePresent, fromStorage.WeightedScore != nil)
				require.Equal(t, "azure-blob", fromStorage.Source)
			})
		}
	}
}

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
