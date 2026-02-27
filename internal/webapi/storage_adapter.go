package webapi

import (
	"context"
	"time"

	"github.com/spboyer/waza/internal/storage"
)

// StorageAdapter adapts storage.ResultStore to the webapi.RunStore interface.
// It provides a bridge between the storage layer and the web API layer.
type StorageAdapter struct {
	store  storage.ResultStore
	source string // "local" or "azure-blob"
}

// NewStorageAdapter creates a RunStore backed by the given storage.ResultStore.
func NewStorageAdapter(store storage.ResultStore, source string) *StorageAdapter {
	return &StorageAdapter{
		store:  store,
		source: source,
	}
}

// ListRuns returns all runs, sorted by the given field and order.
func (sa *StorageAdapter) ListRuns(sortField, order string) ([]RunSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fetch all results from storage.
	results, err := sa.store.List(ctx, storage.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Convert storage.ResultSummary to webapi.RunSummary.
	runs := make([]RunSummary, 0, len(results))
	for _, r := range results {
		runs = append(runs, resultSummaryToRunSummary(r, sa.source))
	}

	// Sort according to parameters.
	sortRuns(runs, sortField, order)
	return runs, nil
}

// GetRun returns a single run with full task details.
func (sa *StorageAdapter) GetRun(id string) (*RunDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := sa.store.Download(ctx, id)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, ErrRunNotFound
		}
		return nil, err
	}

	return outcomeToDetail(outcome), nil
}

// Summary returns aggregate metrics across all runs.
func (sa *StorageAdapter) Summary() (*SummaryResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := sa.store.List(ctx, storage.ListOptions{})
	if err != nil {
		return nil, err
	}

	resp := &SummaryResponse{}
	if len(results) == 0 {
		return resp, nil
	}

	totalTokens := 0
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0

	// We need to download outcomes to get accurate metrics.
	// For performance, we'll just use what we have in ResultSummary for now.
	for range results {
		resp.TotalRuns++
		// Approximate token count (not available in ResultSummary).
		// For accurate metrics, we'd need to download each outcome.
	}

	// Fallback: download all outcomes for accurate metrics.
	for _, r := range results {
		outcome, err := sa.store.Download(ctx, r.RunID)
		if err != nil {
			continue
		}

		totalTasks += outcome.Digest.TotalTests
		totalPassed += outcome.Digest.Succeeded

		s := outcomeToSummary(outcome)
		totalTokens += s.Tokens
		totalCost += s.Cost
		totalDuration += s.Duration
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

// resultSummaryToRunSummary converts storage.ResultSummary to webapi.RunSummary.
func resultSummaryToRunSummary(r storage.ResultSummary, source string) RunSummary {
	outcome := "passed"
	if r.PassRate < 100.0 {
		outcome = "failed"
	}

	return RunSummary{
		ID:         r.RunID,
		Spec:       r.Skill,
		Model:      r.Model,
		JudgeModel: "",
		Outcome:    outcome,
		PassCount:  0, // Not available in ResultSummary
		TaskCount:  0, // Not available in ResultSummary
		Tokens:     0, // Not available in ResultSummary
		Cost:       0, // Not available in ResultSummary
		Duration:   0, // Not available in ResultSummary
		Timestamp:  r.Timestamp,
		Source:     source,
	}
}

// Ensure StorageAdapter satisfies RunStore.
var _ RunStore = (*StorageAdapter)(nil)
