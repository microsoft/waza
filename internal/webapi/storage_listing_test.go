package webapi

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/storage"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type listedResultStore struct {
	storage.ResultStore
	gate    <-chan struct{}
	started chan struct{}
	calls   atomic.Int32
	active  atomic.Int32
	peak    atomic.Int32
	failID  string
}

func (s *listedResultStore) List(context.Context, storage.ListOptions) ([]storage.ResultSummary, error) {
	results := make([]storage.ResultSummary, 64)
	for i := range results {
		id := fmt.Sprintf("run-%02d", i)
		results[i] = storage.ResultSummary{RunID: id, BlobPath: "skill/" + id + ".json"}
	}
	return results, nil
}

func (s *listedResultStore) Download(context.Context, string) (*models.EvaluationOutcome, error) {
	return nil, errors.New("unexpected run ID lookup")
}

func (s *listedResultStore) DownloadListedResult(ctx context.Context, result storage.ResultSummary) (*models.EvaluationOutcome, error) {
	s.calls.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for peak := s.peak.Load(); active > peak; peak = s.peak.Load() {
		if s.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	s.started <- struct{}{}
	if result.RunID == s.failID {
		return nil, errors.New("listed download failed")
	}
	select {
	case <-s.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if result.BlobPath != "skill/"+result.RunID+".json" {
		return nil, errors.New("listed path lost")
	}
	outcome := creditsOutcome(result.RunID, &models.UsageStats{AICredits: utils.Ptr(1.0)})
	return &outcome, nil
}

func TestStorageAdapterBoundsDirectDownloads(t *testing.T) {
	gate := make(chan struct{})
	store := &listedResultStore{gate: gate, started: make(chan struct{}, 128)}
	adapter := NewStorageAdapter(store, "azure-blob")
	done := make(chan error, 1)
	go func() {
		runs, err := adapter.ListRuns("timestamp", "asc")
		if err == nil && len(runs) != 64 {
			err = fmt.Errorf("got %d runs, want 64", len(runs))
		}
		done <- err
	}()
	opened := false
	t.Cleanup(func() {
		if !opened {
			close(gate)
		}
	})
	for range 8 {
		select {
		case <-store.started:
		case <-time.After(5 * time.Second):
			t.Fatal("eight concurrent downloads did not start")
		}
	}
	require.Equal(t, int32(8), store.peak.Load())
	close(gate)
	opened = true
	require.NoError(t, <-done)
	require.Equal(t, int32(64), store.calls.Load())
	require.Equal(t, int32(8), store.peak.Load())
	summary, err := adapter.Summary()
	require.NoError(t, err)
	require.Equal(t, 64, summary.TotalRuns)
	require.Equal(t, 1.0, *summary.AvgAICredits)
	require.Equal(t, int32(128), store.calls.Load())
	require.LessOrEqual(t, store.peak.Load(), int32(8))
}

func TestStorageAdapterDirectDownloadFailureCancelsWorkers(t *testing.T) {
	store := &listedResultStore{gate: make(chan struct{}), started: make(chan struct{}, 128), failID: "run-00"}
	adapter := NewStorageAdapter(store, "azure-blob")
	_, err := adapter.ListRuns("timestamp", "asc")
	require.ErrorContains(t, err, `loading run "run-00": listed download failed`)
	require.Zero(t, store.active.Load())
	require.LessOrEqual(t, store.calls.Load(), int32(8))
}

func TestStorageAdapterListedLoadingCancellationAndEmpty(t *testing.T) {
	store := &listedResultStore{gate: make(chan struct{}), started: make(chan struct{}, 128)}
	adapter := NewStorageAdapter(store, "azure-blob")
	summaries, err := adapter.loadSummaries(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, summaries)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err := store.List(t.Context(), storage.ListOptions{})
	require.NoError(t, err)
	_, err = adapter.loadSummaries(ctx, results)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, store.active.Load())
}
