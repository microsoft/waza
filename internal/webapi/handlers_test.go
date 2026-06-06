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
	"github.com/microsoft/waza/internal/pricing"
)

// mockStore implements RunStore for testing.
type mockStore struct {
	runs    map[string]*RunDetail
	listErr error
	getErr  error
	sumErr  error
}

func newMockStore() *mockStore {
	return &mockStore{runs: make(map[string]*RunDetail)}
}

func (m *mockStore) addRun(detail *RunDetail) {
	m.runs[detail.ID] = detail
}

func (m *mockStore) ListRuns(sortField, order string) ([]RunSummary, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	runs := make([]RunSummary, 0, len(m.runs))
	for _, d := range m.runs {
		runs = append(runs, d.RunSummary)
	}
	sortRuns(runs, sortField, order)
	return runs, nil
}

func (m *mockStore) GetRun(id string) (*RunDetail, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	d, ok := m.runs[id]
	if !ok {
		return nil, ErrRunNotFound
	}
	return d, nil
}

func (m *mockStore) Summary() (*SummaryResponse, error) {
	if m.sumErr != nil {
		return nil, m.sumErr
	}
	resp := &SummaryResponse{}
	totalTokens := 0
	totalPremium := 0.0
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0
	costSources := make([]string, 0, len(m.runs))

	for _, d := range m.runs {
		resp.TotalRuns++
		totalTasks += d.TaskCount
		totalPassed += d.PassCount
		totalTokens += d.Tokens
		totalPremium += d.PremiumRequests
		totalCost += d.Cost
		totalDuration += d.Duration
		costSources = append(costSources, d.CostSource)
	}

	resp.TotalTasks = totalTasks
	if totalTasks > 0 {
		resp.PassRate = float64(totalPassed) / float64(totalTasks) * 100.0
	}
	if resp.TotalRuns > 0 {
		resp.AvgTokens = float64(totalTokens) / float64(resp.TotalRuns)
		resp.AvgPremiumRequests = totalPremium / float64(resp.TotalRuns)
		resp.AvgCost = totalCost / float64(resp.TotalRuns)
		resp.AvgDuration = totalDuration / float64(resp.TotalRuns)
	}
	resp.CostSource = pricing.CombineSources(costSources)

	return resp, nil
}

func sampleRun(id, spec, model string, passed, total int, tokens int, ts time.Time) *RunDetail {
	outcome := "passed"
	if passed < total {
		outcome = "failed"
	}
	cost, src := pricing.Compute(&models.UsageStats{
		InputTokens: tokens,
	})
	return &RunDetail{
		RunSummary: RunSummary{
			ID:              id,
			Spec:            spec,
			Model:           model,
			Outcome:         outcome,
			PassCount:       passed,
			TaskCount:       total,
			Tokens:          tokens,
			PremiumRequests: float64(passed),
			Cost:            cost,
			CostSource:      src,
			Duration:        192.5,
			Timestamp:       ts,
		},
		Tasks: []TaskResult{
			{
				Name:     "explain-fibonacci",
				Outcome:  "passed",
				Score:    0.95,
				Duration: 38.2,
				GraderResults: []GraderResult{
					{
						Name:    "code_validator",
						Type:    "code",
						Passed:  true,
						Score:   1.0,
						Message: "All assertions passed",
					},
				},
			},
		},
	}
}

func TestHandleHealth(t *testing.T) {
	store := newMockStore()
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	h.HandleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.Version == "" {
		t.Error("expected non-empty version")
	}
}

func TestHandleSummaryEmpty(t *testing.T) {
	store := newMockStore()
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec := httptest.NewRecorder()

	h.HandleSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp SummaryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalRuns != 0 {
		t.Errorf("expected 0 runs, got %d", resp.TotalRuns)
	}
}

func TestHandleSummaryWithRuns(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)
	store.addRun(sampleRun("r1", "code-explainer", "claude-4.6", 4, 5, 12000, ts))
	store.addRun(sampleRun("r2", "code-explainer", "gpt-4o", 5, 5, 8000, ts.Add(time.Hour)))
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec := httptest.NewRecorder()

	h.HandleSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp SummaryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalRuns != 2 {
		t.Errorf("expected 2 runs, got %d", resp.TotalRuns)
	}
	if resp.TotalTasks != 10 {
		t.Errorf("expected 10 tasks, got %d", resp.TotalTasks)
	}
	if resp.PassRate != 90.0 {
		t.Errorf("expected 90%% pass rate, got %.1f", resp.PassRate)
	}
	// sampleRun assigns PremiumRequests = float64(passed) so the two
	// fixtures (r1=4, r2=5) average to 4.5.
	if resp.AvgPremiumRequests != 4.5 {
		t.Errorf("expected AvgPremiumRequests=4.5, got %f", resp.AvgPremiumRequests)
	}
}

func TestHandleSummaryCostSource(t *testing.T) {
	// Build two runs with explicit, distinguishable cost sources.
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)

	// Run with SDK-reported cost.
	sdkRun := sampleRun("sdk-run", "code-explainer", "claude-opus-4.6", 5, 5, 1000, ts)
	sdkRun.Cost, sdkRun.CostSource = 1.23, pricing.SourceSDK

	// Run priced from rate table.
	tableRun := sampleRun("table-run", "code-explainer", "gpt-4o", 5, 5, 1000, ts.Add(time.Hour))
	tableRun.Cost, tableRun.CostSource = 0.05, pricing.SourceTable

	tests := []struct {
		name     string
		runs     []*RunDetail
		expected string
	}{
		{"all sdk", []*RunDetail{sdkRun}, pricing.SourceSDK},
		{"all table", []*RunDetail{tableRun}, pricing.SourceTable},
		{"mixed sdk and table", []*RunDetail{sdkRun, tableRun}, pricing.SourceMixed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			for _, r := range tt.runs {
				store.addRun(r)
			}
			h := NewHandlers(store)

			req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
			rec := httptest.NewRecorder()
			h.HandleSummary(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			var resp SummaryResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if resp.CostSource != tt.expected {
				t.Errorf("expected costSource=%q, got %q", tt.expected, resp.CostSource)
			}
		})
	}
}

func TestHandleRunsCostSourceInJSON(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)
	run := sampleRun("r1", "code-explainer", "gpt-4o", 5, 5, 1000, ts)
	run.CostSource = pricing.SourceTable
	store.addRun(run)
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec := httptest.NewRecorder()
	h.HandleRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Decode as raw to ensure the field appears in the JSON.
	var raw []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 run, got %d", len(raw))
	}
	if got, _ := raw[0]["costSource"].(string); got != pricing.SourceTable {
		t.Errorf("expected costSource=%q in JSON, got %v", pricing.SourceTable, raw[0]["costSource"])
	}
}

func TestHandleRunsEmpty(t *testing.T) {
	store := newMockStore()
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec := httptest.NewRecorder()

	h.HandleRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var runs []RunSummary
	if err := json.NewDecoder(rec.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestHandleRunsWithSort(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)
	store.addRun(sampleRun("r1", "code-explainer", "claude-4.6", 4, 5, 12000, ts))
	store.addRun(sampleRun("r2", "code-explainer", "gpt-4o", 5, 5, 8000, ts.Add(time.Hour)))
	h := NewHandlers(store)

	tests := []struct {
		name    string
		sort    string
		order   string
		firstID string
	}{
		{"default desc", "", "", "r2"},
		{"timestamp asc", "timestamp", "asc", "r1"},
		{"tokens desc", "tokens", "desc", "r1"},
		{"tokens asc", "tokens", "asc", "r2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/runs"
			if tt.sort != "" || tt.order != "" {
				url += "?"
				if tt.sort != "" {
					url += "sort=" + tt.sort
				}
				if tt.order != "" {
					if tt.sort != "" {
						url += "&"
					}
					url += "order=" + tt.order
				}
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()

			h.HandleRuns(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}

			var runs []RunSummary
			if err := json.NewDecoder(rec.Body).Decode(&runs); err != nil {
				t.Fatal(err)
			}
			if len(runs) != 2 {
				t.Fatalf("expected 2 runs, got %d", len(runs))
			}
			if runs[0].ID != tt.firstID {
				t.Errorf("expected first run %q, got %q", tt.firstID, runs[0].ID)
			}
		})
	}
}

func TestHandleRunDetail(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)
	store.addRun(sampleRun("a3f2b1", "code-explainer", "claude-4.6", 4, 5, 12041, ts))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs/a3f2b1", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var detail RunDetail
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "a3f2b1" {
		t.Errorf("expected id a3f2b1, got %q", detail.ID)
	}
	if len(detail.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(detail.Tasks))
	}
	if detail.Tasks[0].Name != "explain-fibonacci" {
		t.Errorf("expected task name explain-fibonacci, got %q", detail.Tasks[0].Name)
	}
	if len(detail.Tasks[0].GraderResults) != 1 {
		t.Fatalf("expected 1 grader result, got %d", len(detail.Tasks[0].GraderResults))
	}
}

func TestHandleRunDetailNotFound(t *testing.T) {
	store := newMockStore()

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs/nonexistent", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Code != 404 {
		t.Errorf("expected error code 404, got %d", errResp.Code)
	}
}

func TestCORSMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no origins configured means no CORS header", func(t *testing.T) {
		handler := CORSMiddleware(inner)
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "http://evil.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("expected no CORS header when no origins configured")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("allowed origin gets CORS header", func(t *testing.T) {
		handler := CORSMiddleware(inner, "http://localhost:5173")
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
			t.Error("expected CORS header for allowed origin")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("disallowed origin gets no CORS header", func(t *testing.T) {
		handler := CORSMiddleware(inner, "http://localhost:5173")
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "http://evil.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("expected no CORS header for disallowed origin")
		}
	})

	t.Run("OPTIONS preflight", func(t *testing.T) {
		handler := CORSMiddleware(inner, "http://localhost:5173")
		req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
		}
	})
}

func TestRegisterRoutes(t *testing.T) {
	store := newMockStore()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	// Verify health endpoint is wired up.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /api/health, got %d", rec.Code)
	}

	// Verify summary endpoint is wired up.
	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /api/summary, got %d", rec.Code)
	}

	// Verify runs endpoint is wired up.
	req = httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /api/runs, got %d", rec.Code)
	}
}

func TestFileStoreEmptyDir(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}

	summary, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRuns != 0 {
		t.Errorf("expected 0 runs, got %d", summary.TotalRuns)
	}
}

func TestFileStoreNonexistentDir(t *testing.T) {
	store := NewFileStore("/tmp/nonexistent-waza-dir-test-12345")

	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestSummaryError(t *testing.T) {
	store := newMockStore()
	store.sumErr = fmt.Errorf("boom")
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec := httptest.NewRecorder()

	h.HandleSummary(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestHandleRunDetailStoreError(t *testing.T) {
	store := newMockStore()
	store.getErr = fmt.Errorf("disk I/O error")

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs/any-id", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for store error, got %d", rec.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Code != 500 {
		t.Errorf("expected error code 500, got %d", errResp.Code)
	}
}

func TestHandleRunDetailWithTranscriptAndDigest(t *testing.T) {
	store := newMockStore()
	ts := time.Date(2026, 2, 18, 15, 30, 0, 0, time.UTC)

	success := true
	detail := sampleRun("t1", "code-explainer", "claude-4.6", 1, 1, 5000, ts)
	detail.Tasks[0].Transcript = []TranscriptEventResponse{
		{
			Type:    "assistant.message",
			Content: "Here is the explanation",
		},
		{
			Type:       "tool.execution.start",
			ToolCallID: "call_123",
			ToolName:   "bash",
			Arguments:  map[string]any{"command": "echo hello"},
		},
		{
			Type:       "tool.execution.complete",
			ToolCallID: "call_123",
			ToolName:   "bash",
			ToolResult: map[string]any{"output": "hello"},
			Success:    &success,
		},
	}
	detail.Tasks[0].SessionDigest = &SessionDigestResponse{
		TotalTurns:    3,
		ToolCallCount: 1,
		TokensIn:      2000,
		TokensOut:     3000,
		TokensTotal:   5000,
		ToolsUsed:     []string{"bash"},
		Errors:        []string{},
	}
	store.addRun(detail)

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runs/t1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result RunDetail
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(result.Tasks))
	}

	task := result.Tasks[0]

	// Verify transcript
	if len(task.Transcript) != 3 {
		t.Fatalf("expected 3 transcript events, got %d", len(task.Transcript))
	}
	if task.Transcript[0].Type != "assistant.message" {
		t.Errorf("expected first event type assistant.message, got %q", task.Transcript[0].Type)
	}
	if task.Transcript[0].Content != "Here is the explanation" {
		t.Errorf("expected content 'Here is the explanation', got %q", task.Transcript[0].Content)
	}
	if task.Transcript[1].ToolName != "bash" {
		t.Errorf("expected tool name bash, got %q", task.Transcript[1].ToolName)
	}
	if task.Transcript[2].Success == nil || !*task.Transcript[2].Success {
		t.Error("expected tool execution success to be true")
	}

	// Verify session digest
	if task.SessionDigest == nil {
		t.Fatal("expected session digest to be present")
	}
	if task.SessionDigest.TotalTurns != 3 {
		t.Errorf("expected 3 total turns, got %d", task.SessionDigest.TotalTurns)
	}
	if task.SessionDigest.ToolCallCount != 1 {
		t.Errorf("expected 1 tool call, got %d", task.SessionDigest.ToolCallCount)
	}
	if task.SessionDigest.TokensTotal != 5000 {
		t.Errorf("expected 5000 total tokens, got %d", task.SessionDigest.TokensTotal)
	}
	if len(task.SessionDigest.ToolsUsed) != 1 || task.SessionDigest.ToolsUsed[0] != "bash" {
		t.Errorf("expected tools used [bash], got %v", task.SessionDigest.ToolsUsed)
	}
}

func TestFileStoreRecursiveScanning(t *testing.T) {
	dir := t.TempDir()

	// Create nested directory structure
	subdir1 := filepath.Join(dir, "code-explainer")
	subdir2 := filepath.Join(dir, "code-reviewer")
	if err := os.MkdirAll(subdir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subdir2, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create valid outcome files in subdirectories
	outcome1 := `{
		"eval_name": "code-explainer",
		"timestamp": "2026-02-18T15:30:00Z",
		"config": {"model_id": "gpt-4o"},
		"summary": {"total_tests": 5, "succeeded": 5}
	}`
	outcome2 := `{
		"eval_name": "code-reviewer",
		"timestamp": "2026-02-18T16:30:00Z",
		"config": {"model_id": "claude-4.6"},
		"summary": {"total_tests": 3, "succeeded": 2}
	}`

	if err := os.WriteFile(filepath.Join(subdir1, "gpt-4o.json"), []byte(outcome1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir2, "claude-4.6.json"), []byte(outcome2), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(dir)
	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 2 {
		t.Fatalf("expected 2 runs from nested dirs, got %d", len(runs))
	}

	// Verify RunIDs use relative paths
	foundExplainer := false
	foundReviewer := false
	for _, run := range runs {
		if run.ID == "code-explainer/gpt-4o" {
			foundExplainer = true
			if run.Spec != "code-explainer" {
				t.Errorf("expected spec code-explainer, got %q", run.Spec)
			}
		}
		if run.ID == "code-reviewer/claude-4.6" {
			foundReviewer = true
			if run.Spec != "code-reviewer" {
				t.Errorf("expected spec code-reviewer, got %q", run.Spec)
			}
		}
	}

	if !foundExplainer {
		t.Error("expected to find code-explainer/gpt-4o")
	}
	if !foundReviewer {
		t.Error("expected to find code-reviewer/claude-4.6")
	}
}

func TestFileStoreIgnoresSummaryJson(t *testing.T) {
	dir := t.TempDir()

	// Create a valid outcome
	validOutcome := `{
		"eval_name": "test-bench",
		"timestamp": "2026-02-18T15:30:00Z",
		"config": {"model_id": "gpt-4o"},
		"summary": {"total_tests": 1, "succeeded": 1}
	}`

	// Create summary.json (should be ignored)
	summaryJson := `{
		"aggregate_pass_rate": 0.95,
		"total_runs": 3
	}`

	if err := os.WriteFile(filepath.Join(dir, "gpt-4o.json"), []byte(validOutcome), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(summaryJson), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(dir)
	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 1 {
		t.Fatalf("expected 1 run (summary.json should be ignored), got %d", len(runs))
	}
	if runs[0].Spec != "test-bench" {
		t.Errorf("expected valid outcome, got %v", runs[0])
	}
}

func TestFileStoreIgnoresInvalidJson(t *testing.T) {
	dir := t.TempDir()

	// Create files with various issues
	validOutcome := `{
		"eval_name": "valid",
		"summary": {"total_tests": 1}
	}`
	invalidJson := `{ this is not valid json }`
	emptyOutcome := `{}`
	notJson := `Just some text`

	files := map[string]string{
		"valid.json":   validOutcome,
		"invalid.json": invalidJson,
		"empty.json":   emptyOutcome,
		"text.json":    notJson,
		"readme.txt":   "Should be skipped (not .json)",
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store := NewFileStore(dir)
	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 1 {
		t.Fatalf("expected 1 valid run, got %d", len(runs))
	}
	if runs[0].Spec != "valid" {
		t.Errorf("expected valid spec, got %q", runs[0].Spec)
	}
}

func TestFileStoreDeepNesting(t *testing.T) {
	dir := t.TempDir()

	// Create deeply nested structure
	deepPath := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(deepPath, 0o755); err != nil {
		t.Fatal(err)
	}

	outcome := `{
		"eval_name": "deep-test",
		"summary": {"total_tests": 1, "succeeded": 1}
	}`

	if err := os.WriteFile(filepath.Join(deepPath, "result.json"), []byte(outcome), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(dir)
	runs, err := store.ListRuns("", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 1 {
		t.Fatalf("expected 1 run from deep nesting, got %d", len(runs))
	}
	if runs[0].ID != "a/b/c/result" {
		t.Errorf("expected ID 'a/b/c/result', got %q", runs[0].ID)
	}
}
