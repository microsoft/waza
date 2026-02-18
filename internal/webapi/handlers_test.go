package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0

	for _, d := range m.runs {
		resp.TotalRuns++
		totalTasks += d.TaskCount
		totalPassed += d.PassCount
		totalTokens += d.Tokens
		totalCost += d.Cost
		totalDuration += d.Duration
	}

	resp.TotalTasks = totalTasks
	if totalTasks > 0 {
		resp.PassRate = float64(totalPassed) / float64(totalTasks) * 100.0
	}
	if resp.TotalRuns > 0 {
		resp.AvgTokens = float64(totalTokens) / float64(resp.TotalRuns)
		resp.AvgCost = totalCost / float64(resp.TotalRuns)
		resp.AvgDuration = totalDuration / float64(resp.TotalRuns)
	}

	return resp, nil
}

func sampleRun(id, spec, model string, passed, total int, tokens int, ts time.Time) *RunDetail {
	outcome := "passed"
	if passed < total {
		outcome = "failed"
	}
	return &RunDetail{
		RunSummary: RunSummary{
			ID:        id,
			Spec:      spec,
			Model:     model,
			Outcome:   outcome,
			PassCount: passed,
			TaskCount: total,
			Tokens:    tokens,
			Cost:      float64(tokens) * 0.00025,
			Duration:  192.5,
			Timestamp: ts,
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
