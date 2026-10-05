package webapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/pricing"
	"github.com/microsoft/waza/internal/storage"
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

	runs, err := sa.loadSummaries(ctx, results)
	if err != nil {
		return nil, err
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

	summaries, err := sa.loadSummaries(ctx, results)
	if err != nil {
		return nil, err
	}

	totalTokens := 0
	totalPremium := 0.0
	credits := aiCreditAccumulator{}
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0
	costSources := make([]string, 0, len(results))

	resp.TotalRuns = len(results)

	// Listing metadata has no usage; download outcomes for complete metrics.
	for _, s := range summaries {
		totalTasks += s.TaskCount
		totalPassed += s.PassCount
		totalTokens += s.Tokens
		totalPremium += s.PremiumRequests
		credits.add(s.AICredits)
		totalCost += s.Cost
		totalDuration += s.Duration
		costSources = append(costSources, s.CostSource)
	}

	resp.TotalTasks = totalTasks
	if totalTasks > 0 {
		resp.PassRate = float64(totalPassed) / float64(totalTasks) * 100.0
	}
	if resp.TotalRuns > 0 {
		resp.AvgTokens = float64(totalTokens) / float64(resp.TotalRuns)
		resp.AvgPremiumRequests = totalPremium / float64(resp.TotalRuns)
		resp.AvgAICredits = credits.average()
		resp.AvgCost = totalCost / float64(resp.TotalRuns)
		resp.AvgDuration = totalDuration / float64(resp.TotalRuns)
	}
	resp.CostSource = pricing.CombineSources(costSources)

	return resp, nil
}

func (sa *StorageAdapter) loadSummaries(ctx context.Context, results []storage.ResultSummary) ([]RunSummary, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	summaries := make([]RunSummary, len(results))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(8, len(results)) {
		workers.Go(func() {
			for i := range jobs {
				outcome, err := storage.DownloadListedResult(ctx, sa.store, results[i])
				if err != nil {
					cancel(fmt.Errorf("loading run %q: %w", results[i].RunID, err))
					return
				}
				summary := outcomeToSummary(outcome)
				summary.Source = sa.source
				summaries[i] = summary
			}
		})
	}
dispatch:
	for i := range results {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return summaries, nil
}

// Ensure StorageAdapter satisfies RunStore.
var _ RunStore = (*StorageAdapter)(nil)
