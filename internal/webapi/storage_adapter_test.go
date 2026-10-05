package webapi

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/storage"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type failingResultStore struct {
	storage.ResultStore
	listErr error
}

func (s failingResultStore) List(context.Context, storage.ListOptions) ([]storage.ResultSummary, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return []storage.ResultSummary{{RunID: "missing"}}, nil
}

func (s failingResultStore) Download(context.Context, string) (*models.EvaluationOutcome, error) {
	return nil, errors.New("download failed")
}

func TestStorageAdapterListsCompleteUsage(t *testing.T) {
	store := storage.NewLocalStore(t.TempDir())
	for _, outcome := range []models.EvaluationOutcome{
		creditsOutcome("reported", &models.UsageStats{AICredits: utils.Ptr(1.123456789), ModelMetrics: map[string]models.ModelUsage{
			"gpt-4o": {AICredits: utils.Ptr(1.123456789), InputTokens: 2},
		}}),
		creditsOutcome("legacy", &models.UsageStats{InputTokens: 3}),
	} {
		require.NoError(t, store.Upload(t.Context(), &outcome))
	}
	adapter := NewStorageAdapter(store, "azure-blob")
	runs, err := adapter.ListRuns("timestamp", "asc")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	for _, run := range runs {
		require.Equal(t, "azure-blob", run.Source)
		if run.ID == "reported" {
			require.Equal(t, 1.123456789, *run.AICredits)
			require.Equal(t, 1.123456789, *run.ModelUsage[0].AICredits)
			require.Equal(t, 2, run.ModelUsage[0].InputTokens)
		} else {
			require.Nil(t, run.AICredits)
		}
	}
	summary, err := adapter.Summary()
	require.NoError(t, err)
	require.Equal(t, 1.123456789, *summary.AvgAICredits)
}

func TestStorageAdapterUsageFailuresSurface(t *testing.T) {
	for _, listErr := range []error{nil, errors.New("list failed")} {
		adapter := NewStorageAdapter(failingResultStore{listErr: listErr}, "test")
		_, err := adapter.ListRuns("timestamp", "asc")
		require.Error(t, err)
		_, err = adapter.Summary()
		require.Error(t, err)
		if listErr != nil {
			require.ErrorIs(t, err, listErr)
		} else {
			require.ErrorContains(t, err, `loading run "missing": download failed`)
		}
	}
}
