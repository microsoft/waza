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
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/statistics"
)

func writeOutcomeFile(t *testing.T, path string, outcome models.EvaluationOutcome) {
	t.Helper()

	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write outcome file: %v", err)
	}
}

func TestHandleRunsStoreError(t *testing.T) {
	store := newMockStore()
	store.listErr = errors.New("list failed")
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec := httptest.NewRecorder()
	h.HandleRuns(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Code != http.StatusInternalServerError {
		t.Errorf("expected error code 500, got %d", errResp.Code)
	}
	if !strings.Contains(errResp.Error, "list failed") {
		t.Errorf("expected error message to contain list failed, got %q", errResp.Error)
	}
}

func TestHandleRunDetailMissingID(t *testing.T) {
	h := NewHandlers(newMockStore())

	req := httptest.NewRequest(http.MethodGet, "/api/runs/", nil)
	rec := httptest.NewRecorder()
	h.HandleRunDetail(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleRunDetailFallbackPathExtraction(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)
	store.addRun(sampleRun("fallback-id", "code-explainer", "gpt-4o", 1, 1, 1000, ts))
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs/fallback-id/more", nil)
	rec := httptest.NewRecorder()
	h.HandleRunDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var detail RunDetail
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "fallback-id" {
		t.Errorf("expected fallback-id, got %q", detail.ID)
	}
}

func TestFileStoreGetRunSummaryAndReload(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC)

	outcome1 := models.EvaluationOutcome{
		RunID:     "run-1",
		BenchName: "bench-a",
		Timestamp: ts,
		Setup:     models.OutcomeSetup{ModelID: "gpt-4o"},
		Digest:    models.OutcomeDigest{TotalTests: 2, Succeeded: 1, Failed: 1, DurationMs: 3000},
		TestOutcomes: []models.TestOutcome{
			{
				DisplayName: "task-a",
				Status:      models.StatusFailed,
				Runs: []models.RunResult{
					{
						DurationMs: 1400,
						Validations: map[string]models.GraderResults{
							"text": {Name: "text", Type: models.GraderKindText, Passed: false, Score: 0.2, Weight: 1, Feedback: "failed"},
						},
					},
				},
			},
		},
	}

	writeOutcomeFile(t, filepath.Join(dir, "run-1.json"), outcome1)
	store := NewFileStore(dir)

	detail, err := store.GetRun("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != "run-1" {
		t.Errorf("expected run-1, got %q", detail.ID)
	}

	summary, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRuns != 1 {
		t.Errorf("expected 1 run, got %d", summary.TotalRuns)
	}
	if summary.TotalTasks != 2 {
		t.Errorf("expected 2 tasks, got %d", summary.TotalTasks)
	}
	if summary.PassRate != 50.0 {
		t.Errorf("expected 50%% pass rate, got %.1f", summary.PassRate)
	}

	if _, err := store.GetRun("missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}

	outcome2 := models.EvaluationOutcome{
		BenchName: "bench-b",
		Timestamp: ts.Add(time.Hour),
		Setup:     models.OutcomeSetup{ModelID: "claude-4.6"},
		Digest:    models.OutcomeDigest{TotalTests: 1, Succeeded: 1, DurationMs: 1000},
	}
	writeOutcomeFile(t, filepath.Join(dir, "nested", "run-2.json"), outcome2)

	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns("timestamp", "asc")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs after reload, got %d", len(runs))
	}
	if runs[0].ID != "run-1" {
		t.Errorf("expected first run run-1, got %q", runs[0].ID)
	}
	if runs[1].ID != "nested/run-2" {
		t.Errorf("expected second run nested/run-2, got %q", runs[1].ID)
	}
}

func TestOutcomeToDetailMapsStatsTranscriptAndDigest(t *testing.T) {
	significant := true
	success := true
	content := "assistant output"
	message := "tool complete"
	toolCallID := "call-123"
	toolName := "bash"

	outcome := &models.EvaluationOutcome{
		RunID:     "detail-run",
		BenchName: "bench-detail",
		Setup:     models.OutcomeSetup{ModelID: "gpt-4o", JudgeModel: "o3"},
		Digest:    models.OutcomeDigest{TotalTests: 2, Succeeded: 1, Failed: 1, DurationMs: 4000},
		TestOutcomes: []models.TestOutcome{
			{
				DisplayName: "task-with-data",
				Status:      models.StatusFailed,
				Stats: &models.TestStats{
					AvgScore:      0.3,
					AvgDurationMs: 2500,
					BootstrapCI: &statistics.ConfidenceInterval{
						Lower:           0.1,
						Upper:           0.5,
						Mean:            0.3,
						ConfidenceLevel: 0.95,
					},
					IsSignificant: &significant,
				},
				Runs: []models.RunResult{
					{
						DurationMs: 1500,
						Validations: map[string]models.GraderResults{
							"code": {Name: "code", Type: models.GraderKindInlineScript, Passed: false, Score: 0.2, Weight: 1, Feedback: "failed"},
						},
						Transcript: []models.TranscriptEvent{
							{
								SessionEvent: copilot.SessionEvent{
									Type: copilot.ToolExecutionComplete,
									Data: copilot.Data{
										Content:    &content,
										Message:    &message,
										ToolCallID: &toolCallID,
										ToolName:   &toolName,
										Arguments:  map[string]any{"command": "echo hi"},
										Result:     &copilot.Result{Content: "hi"},
										Success:    &success,
									},
								},
							},
						},
						SessionDigest: models.SessionDigest{
							TotalTurns:    2,
							ToolCallCount: 1,
							TokensIn:      10,
							TokensOut:     20,
							TokensTotal:   30,
						},
					},
				},
			},
			{DisplayName: "task-no-runs", Status: models.StatusPassed},
		},
	}

	detail := outcomeToDetail(outcome)

	if detail.Outcome != "failed" {
		t.Errorf("expected failed outcome, got %q", detail.Outcome)
	}
	if len(detail.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(detail.Tasks))
	}

	taskWithData := detail.Tasks[0]
	if taskWithData.BootstrapCI == nil || taskWithData.BootstrapCI.Mean != 0.3 {
		t.Fatalf("expected bootstrap CI mean 0.3, got %+v", taskWithData.BootstrapCI)
	}
	if taskWithData.IsSignificant == nil || !*taskWithData.IsSignificant {
		t.Fatal("expected significant=true")
	}
	if len(taskWithData.GraderResults) != 1 {
		t.Fatalf("expected 1 grader result, got %d", len(taskWithData.GraderResults))
	}
	if len(taskWithData.Transcript) != 1 {
		t.Fatalf("expected 1 transcript event, got %d", len(taskWithData.Transcript))
	}
	if taskWithData.Transcript[0].ToolCallID != toolCallID {
		t.Errorf("expected tool call id %q, got %q", toolCallID, taskWithData.Transcript[0].ToolCallID)
	}
	if taskWithData.SessionDigest == nil {
		t.Fatal("expected session digest")
	}
	if taskWithData.SessionDigest.ToolsUsed == nil || len(taskWithData.SessionDigest.ToolsUsed) != 0 {
		t.Errorf("expected empty toolsUsed slice, got %v", taskWithData.SessionDigest.ToolsUsed)
	}
	if taskWithData.SessionDigest.Errors == nil || len(taskWithData.SessionDigest.Errors) != 0 {
		t.Errorf("expected empty errors slice, got %v", taskWithData.SessionDigest.Errors)
	}

	taskNoRuns := detail.Tasks[1]
	if len(taskNoRuns.GraderResults) != 0 {
		t.Errorf("expected empty grader results, got %d entries", len(taskNoRuns.GraderResults))
	}
}

func TestOutcomeToDetailNoTasks(t *testing.T) {
	detail := outcomeToDetail(&models.EvaluationOutcome{
		RunID:     "empty",
		BenchName: "bench-empty",
		Setup:     models.OutcomeSetup{ModelID: "gpt-4o"},
	})

	if detail.Tasks == nil {
		t.Fatal("expected non-nil tasks slice")
	}
	if len(detail.Tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(detail.Tasks))
	}
}
