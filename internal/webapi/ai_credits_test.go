package webapi

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

func creditsOutcome(runID string, usage *models.UsageStats) models.EvaluationOutcome {
	return models.EvaluationOutcome{
		RunID:     runID,
		BenchName: "bench-credits",
		Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Setup:     models.OutcomeSetup{ModelID: "gpt-4o"},
		Digest:    models.OutcomeDigest{TotalTests: 1, Succeeded: 1, DurationMs: 1000},
		TestOutcomes: []models.TestOutcome{
			{
				DisplayName: "task-a",
				Status:      models.StatusPassed,
				Runs: []models.RunResult{
					{DurationMs: 1000, SessionDigest: models.SessionDigest{Usage: usage}},
				},
			},
		},
	}
}

func TestOutcomeToSummaryReportsAICreditsAndModelUsage(t *testing.T) {
	usage := &models.UsageStats{
		InputTokens:      1000,
		OutputTokens:     400,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
		AICredits:        utils.Ptr(1.25),
		ModelMetrics: map[string]models.ModelUsage{
			"gpt-4o": {
				InputTokens:      600,
				OutputTokens:     200,
				CacheReadTokens:  200,
				CacheWriteTokens: 100,
				AICredits:        utils.Ptr(0.75),
			},
			"claude-sonnet-4": {
				InputTokens:  400,
				OutputTokens: 200,
				// No AICredits: SDK did not report credits for this model.
			},
		},
	}

	s := outcomeToSummary(utils.Ptr(creditsOutcome("run-credits", usage)))

	if s.AICredits == nil || *s.AICredits != 1.25 {
		t.Fatalf("expected aiCredits 1.25, got %v", s.AICredits)
	}
	if len(s.ModelUsage) != 2 {
		t.Fatalf("expected 2 model usage rows, got %d", len(s.ModelUsage))
	}
	if s.ModelUsage[0].Model != "claude-sonnet-4" || s.ModelUsage[1].Model != "gpt-4o" {
		t.Fatalf("expected model usage sorted by model ID, got %v, %v", s.ModelUsage[0].Model, s.ModelUsage[1].Model)
	}
	if s.ModelUsage[0].AICredits != nil {
		t.Errorf("expected unavailable credits for claude-sonnet-4, got %v", *s.ModelUsage[0].AICredits)
	}
	gpt := s.ModelUsage[1]
	if gpt.AICredits == nil || *gpt.AICredits != 0.75 {
		t.Fatalf("expected gpt-4o credits 0.75, got %v", gpt.AICredits)
	}
	if gpt.InputTokens != 600 || gpt.OutputTokens != 200 || gpt.CacheReadTokens != 200 || gpt.CacheWriteTokens != 100 {
		t.Errorf("unexpected token breakdown: %+v", gpt)
	}
}

func TestOutcomeToSummaryOmitsAICreditsForLegacyArtifacts(t *testing.T) {
	usage := &models.UsageStats{InputTokens: 100, OutputTokens: 50, PremiumRequests: 2}

	s := outcomeToSummary(utils.Ptr(creditsOutcome("run-legacy", usage)))

	if s.AICredits != nil {
		t.Fatalf("expected no aiCredits for legacy artifact, got %v", *s.AICredits)
	}
	if s.PremiumRequests != 2 {
		t.Errorf("expected legacy premiumRequests preserved, got %v", s.PremiumRequests)
	}
}

func TestFileStoreSummaryAveragesAICreditsOverReportingRuns(t *testing.T) {
	dir := t.TempDir()
	withCredits := creditsOutcome("run-1", &models.UsageStats{InputTokens: 100, AICredits: utils.Ptr(2.0)})
	legacy := creditsOutcome("run-2", &models.UsageStats{InputTokens: 100, PremiumRequests: 3})
	writeOutcomeFile(t, filepath.Join(dir, "run-1.json"), withCredits)
	writeOutcomeFile(t, filepath.Join(dir, "run-2.json"), legacy)

	summary, err := NewFileStore(dir).Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.AvgAICredits == nil || *summary.AvgAICredits != 2.0 {
		t.Fatalf("expected avg AI credits 2.0 across reporting runs, got %v", summary.AvgAICredits)
	}
}

func TestFileStoreSummaryOmitsAICreditsWhenNoRunReportsThem(t *testing.T) {
	dir := t.TempDir()
	writeOutcomeFile(t, filepath.Join(dir, "run-1.json"),
		creditsOutcome("run-1", &models.UsageStats{InputTokens: 100, PremiumRequests: 1}))

	summary, err := NewFileStore(dir).Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.AvgAICredits != nil {
		t.Fatalf("expected unavailable avg AI credits, got %v", *summary.AvgAICredits)
	}
}
