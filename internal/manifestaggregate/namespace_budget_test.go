package manifestaggregate

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

type observedRead func(context.Context, string, int64) ([]byte, error)

func (f observedRead) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return f(ctx, key, limit)
}

type observedBackend struct {
	artifactmanifest.Backend
	calls int
}

func (b *observedBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.calls++
	return b.Backend.Get(ctx, key, limit)
}

func TestAggregateBudgetSharesSealedNamespaceAndCompletedStoreReads(t *testing.T) {
	local, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, local.Close()) }()
	namespace, err := artifactmanifest.NewNamespace(local, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	key := "reservation/exec-" + strings.Repeat("a", 64)
	original := []byte("opaque budget control; not an ownership declaration")
	created, err := namespace.Create(t.Context(), key, original)
	require.NoError(t, err)
	require.True(t, created)
	manifest, terminal := fixture(t, 1, 1, runnerterminal.Success)
	digest := publish(t, local, manifest, terminal, true)
	budget := &readBudget{}
	namespaceReads := 0
	reader, err := namespace.Reader(func(raw artifactmanifest.ReadBackend) artifactmanifest.ReadBackend {
		return budget.decorate(observedRead(func(ctx context.Context, key string, limit int64) ([]byte, error) {
			namespaceReads++
			return raw.Get(ctx, key, limit)
		}))
	})
	require.NoError(t, err)
	data, err := reader.ReadUnverified(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, original, data)
	require.EqualValues(t, len(original), budget.read, "success refunds unused namespace reservation")
	data[0] = 'x'
	provider := &observedBackend{Backend: local}
	store := artifactmanifest.NewStore(&boundedBackend{Backend: provider, readBudget: budget})
	completed, err := store.ReadCompleted(t.Context(), digest)
	require.NoError(t, err)
	require.Equal(t, terminal, completed.ArtifactBytes[manifest.TerminalArtifact])
	require.Greater(t, budget.read, int64(len(original)), "both paths charge the same counter")
	require.Positive(t, provider.calls)

	budget.read = MaxReadBytes - artifactmanifest.MaxNamespaceObjectBytes - 1
	_, err = reader.ReadUnverified(t.Context(), "completion/exec-"+strings.Repeat("b", 64))
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	require.EqualValues(t, MaxReadBytes, budget.read, "unknown failed namespace I/O keeps the full reservation")
	beforeNamespace, beforeStore := namespaceReads, provider.calls
	_, err = reader.ReadUnverified(t.Context(), key)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, 1, budget.hits)
	completed, err = store.ReadCompleted(t.Context(), digest)
	require.ErrorIs(t, err, ErrLimit)
	require.Nil(t, completed)
	require.Equal(t, beforeNamespace, namespaceReads, "exhaustion prevents raw namespace I/O")
	require.Equal(t, beforeStore, provider.calls, "exhaustion prevents artifact provider I/O")
	require.Equal(t, 3, budget.hits, "Store checks manifest and final marker independently; both reads are withheld")
	require.Zero(t, budget.unavailable, "budget withholding is not generic storage unavailability")
}

func TestAggregateReadOnlyDecoratorRetainsUnknownFailedConsumption(t *testing.T) {
	provider := &partialFailureBackend{}
	budget := &readBudget{}
	reader := budget.decorate(provider)
	data, err := reader.Get(t.Context(), "reservation/exec-"+strings.Repeat("a", 64), 1024)
	require.Error(t, err)
	require.Nil(t, data)
	require.EqualValues(t, 1025, budget.read)
	require.Equal(t, 1, budget.unavailable)
	require.Equal(t, 1, provider.calls)
}
