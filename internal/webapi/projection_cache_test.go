package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

func projectionCacheOutcome(id string) *models.EvaluationOutcome {
	outcome := &models.EvaluationOutcome{
		RunID:       id,
		SkillTested: "perf-sweep",
		BenchName:   "representative",
		Timestamp:   time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		Setup:       models.OutcomeSetup{ModelID: "gpt-5", RunsPerTest: 2},
		Digest: models.OutcomeDigest{
			TotalTests: 20,
			Succeeded:  18,
			Failed:     2,
			DurationMs: 120_000,
			Usage: &models.UsageStats{
				InputTokens:  20_000,
				OutputTokens: 4_000,
			},
		},
		TestOutcomes: make([]models.TestOutcome, 20),
	}
	for i := range outcome.TestOutcomes {
		outcome.TestOutcomes[i] = models.TestOutcome{
			TestID:      fmt.Sprintf("task-%02d", i),
			DisplayName: fmt.Sprintf("Task %02d", i),
			Status:      models.StatusPassed,
			Runs: []models.RunResult{{
				RunNumber:   1,
				Attempts:    1,
				Prompt:      "Review the representative project and report the result.",
				Status:      models.StatusPassed,
				DurationMs:  6_000,
				FinalOutput: "The representative task completed successfully.",
				Validations: map[string]models.GraderResults{
					"quality": {
						Name:     "quality",
						Type:     models.GraderKindText,
						Score:    1,
						Weight:   1,
						Passed:   true,
						Feedback: "passed",
					},
				},
				SessionDigest: models.SessionDigest{
					ToolCallCount: 4,
					ToolsUsed:     []string{"view", "rg"},
					Usage: &models.UsageStats{
						InputTokens:  1_000,
						OutputTokens: 200,
					},
				},
			}},
		}
	}
	return outcome
}

func projectionCacheFileStore(b *testing.B, count int) *FileStore {
	b.Helper()
	dir := b.TempDir()
	for i := range count {
		outcome := projectionCacheOutcome(fmt.Sprintf("run-%03d", i))
		outcome.Timestamp = outcome.Timestamp.Add(time.Duration(i) * time.Minute)
		data, err := json.Marshal(outcome)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("run-%03d.json", i)), data, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	store := NewFileStore(dir)
	if _, err := store.ListRuns("", ""); err != nil {
		b.Fatal(err)
	}
	return store
}

func BenchmarkPerfSweepWebAPI(b *testing.B) {
	const count = 300
	store := projectionCacheFileStore(b, count)

	b.Run("outcome-to-summary", func(b *testing.B) {
		outcome := projectionCacheOutcome("run")
		b.ReportAllocs()
		for b.Loop() {
			if outcomeToSummary(outcome).ID == "" {
				b.Fatal("missing ID")
			}
		}
	})
	b.Run("outcome-to-detail", func(b *testing.B) {
		outcome := projectionCacheOutcome("run")
		b.ReportAllocs()
		for b.Loop() {
			if len(outcomeToDetail(outcome).Tasks) != 20 {
				b.Fatal("missing tasks")
			}
		}
	})
	b.Run("list-runs-warm", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			runs, err := store.ListRuns("timestamp", "desc")
			if err != nil {
				b.Fatal(err)
			}
			if len(runs) != count {
				b.Fatalf("got %d runs", len(runs))
			}
		}
	})
	b.Run("summary-warm", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			summary, err := store.Summary()
			if err != nil {
				b.Fatal(err)
			}
			if summary.TotalRuns != count {
				b.Fatalf("got %d runs", summary.TotalRuns)
			}
		}
	})
	b.Run("runs-handler-warm", func(b *testing.B) {
		handler := NewHandlers(store)
		request := httptest.NewRequest(http.MethodGet, "/api/runs?sort=timestamp&order=desc", nil)
		b.ReportAllocs()
		for b.Loop() {
			response := httptest.NewRecorder()
			handler.HandleRuns(response, request)
			if response.Code != http.StatusOK {
				b.Fatalf("status %d", response.Code)
			}
		}
	})
}

func TestFileStoreProjectionCacheDoesNotLeakMutableData(t *testing.T) {
	dir := t.TempDir()
	outcome := projectionCacheOutcome("run")
	outcome.Digest.WeightedScore = 0.75
	for i := range outcome.TestOutcomes {
		outcome.TestOutcomes[i].Stats = &models.TestStats{AvgWeightedScore: 0.75}
	}
	outcome.Digest.Usage.AICredits = utils.Ptr(1.25)
	outcome.Digest.Usage.ModelMetrics = map[string]models.ModelUsage{
		"gpt-5": {AICredits: utils.Ptr(1.25), InputTokens: 20_000},
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(dir)

	first, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}
	first[0].ID = "mutated"
	*first[0].AICredits = 99
	*first[0].WeightedScore = 99
	first[0].ModelUsage[0].Model = "mutated"
	*first[0].ModelUsage[0].AICredits = 99

	second, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID != "run" || *second[0].AICredits != 1.25 ||
		*second[0].WeightedScore != 0.75 || second[0].ModelUsage[0].Model != "gpt-5" ||
		*second[0].ModelUsage[0].AICredits != 1.25 {
		t.Fatalf("cached list leaked caller mutations: %+v", second[0])
	}

	summary, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	*summary.AvgAICredits = 99
	summary, err = store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if *summary.AvgAICredits != 1.25 {
		t.Fatalf("cached summary leaked caller mutation: %+v", summary)
	}
}
