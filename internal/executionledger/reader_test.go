package executionledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/stretchr/testify/require"
)

var errReaderBudget = errors.New("reader fixture cumulative budget exhausted")

type readerBudget struct {
	mu        sync.Mutex
	remaining int64
	namespace int
	store     int
	storeKeys []string
}

func (b *readerBudget) get(ctx context.Context, backend artifactmanifest.ReadBackend, key string, limit int64, namespace bool) ([]byte, error) {
	b.mu.Lock()
	if limit+1 > b.remaining {
		b.mu.Unlock()
		return nil, errReaderBudget
	}
	b.remaining -= limit + 1
	if namespace {
		b.namespace++
	} else {
		b.store++
		b.storeKeys = append(b.storeKeys, key)
	}
	b.mu.Unlock()
	data, err := backend.Get(ctx, key, limit)
	if err == nil {
		b.mu.Lock()
		b.remaining += limit + 1 - int64(len(data))
		b.mu.Unlock()
	}
	return data, err
}

type budgetNamespace struct {
	artifactmanifest.ReadBackend
	budget *readerBudget
}

func (r budgetNamespace) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return r.budget.get(ctx, r.ReadBackend, key, limit, true)
}

type budgetStore struct {
	artifactmanifest.Backend
	budget *readerBudget
}

func (b budgetStore) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.budget.get(ctx, b.Backend, key, limit, false)
}

func TestReaderRealNamespaceAndStoreShareInvocationBudget(t *testing.T) {
	ctx := context.Background()
	ledger, backend, _, binding := ownerFixture(t)
	creator, reserved := reserveFixture(t, ledger, binding)
	digest := ownerPublication(t, ledger, binding, true)
	completed, err := ledger.Complete(ctx, creator, digest)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	budget := &readerBudget{remaining: artifactmanifest.MaxTotalBytes}
	scoped, err := namespace.Reader(func(raw artifactmanifest.ReadBackend) artifactmanifest.ReadBackend {
		return budgetNamespace{ReadBackend: raw, budget: budget}
	})
	require.NoError(t, err)
	reader, err := NewReader(scoped, budgetStore{Backend: backend, budget: budget})
	require.NoError(t, err)
	require.Zero(t, budget.namespace)
	require.Zero(t, budget.store, "constructor must not perform ambient/provider reads")
	actual, err := reader.ReadCompleted(ctx, source)
	require.NoError(t, err)
	require.Equal(t, completed.CompletionBytes, actual.CompletionBytes)
	require.Equal(t, reserved.Attempt.Binding, actual.Publication.Manifest.Binding)
	require.Positive(t, budget.namespace)
	require.Positive(t, budget.store)
	actual.CompletionBytes[0] = 'X'
	actual.Reservation.Bytes[0] = 'X'
	again, err := reader.ReadCompleted(ctx, source)
	require.NoError(t, err)
	require.Equal(t, completed.CompletionBytes, again.CompletionBytes)
	require.Equal(t, reserved.Bytes, again.Reservation.Bytes)

	// Both namespace declarations fit, but manifest admission lacks exactly
	// the excess-detection byte. Store may still inspect the small final marker
	// as a diagnostic, but must not read the manifest outside the same cap.
	budget.remaining = int64(len(reserved.Bytes)+len(completed.CompletionBytes)) + artifactmanifest.MaxManifestBytes
	budget.namespace, budget.store = 0, 0
	budget.storeKeys = nil
	actual, err = reader.ReadCompleted(ctx, source)
	require.ErrorIs(t, err, errReaderBudget)
	require.Nil(t, actual)
	require.Equal(t, 2, budget.namespace)
	require.Equal(t, []string{"final-" + digest}, budget.storeKeys)
	require.Less(t, budget.remaining, int64(artifactmanifest.MaxManifestBytes))
	budget.remaining = artifactmanifest.MaxNamespaceObjectBytes + 1
	budget.namespace, budget.store = 0, 0
	budget.storeKeys = nil
	missing := "exec-" + strings.Repeat("d", 64)
	record, err := reader.ReadCompletion(ctx, missing)
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	require.Nil(t, record)
	require.Equal(t, "reading execution completion: missing immutable object", err.Error())
	require.Zero(t, budget.remaining, "failed read consumes the full prior reservation")
	before := budget.namespace
	actual, err = reader.ReadCompleted(ctx, source)
	require.ErrorIs(t, err, errReaderBudget)
	require.Nil(t, actual)
	require.Equal(t, before, budget.namespace)
	require.Zero(t, budget.store)
}

func TestReaderPreservesDeclarationAdmissionAndNoAuthority(t *testing.T) {
	ledger, backend, _, binding := ownerFixture(t)
	_, reserved := reserveFixture(t, ledger, binding)
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	scoped, err := namespace.Reader(func(raw artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return raw })
	require.NoError(t, err)
	reader, err := NewReader(scoped, backend)
	require.NoError(t, err)
	actual, err := reader.ReadReservation(t.Context(), reserved.Attempt.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, reserved.Bytes, actual.Bytes)
	require.Equal(t, reserved.Digest, actual.Digest)
	_, err = reader.ReadReservation(t.Context(), "../escape")
	require.ErrorIs(t, err, artifactmanifest.ErrInvalid)
	record, err := reader.ReadCompletion(t.Context(), reserved.Attempt.ExecutionID)
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	require.Nil(t, record)
	for _, input := range []struct {
		namespace *artifactmanifest.NamespaceReader
		backend   artifactmanifest.Backend
	}{{nil, backend}, {scoped, nil}, {nil, nil}} {
		r, err := NewReader(input.namespace, input.backend)
		require.ErrorIs(t, err, ErrCapability)
		require.Nil(t, r)
	}
	for _, reader := range []*Reader{nil, {}} {
		_, err := reader.ReadReservation(t.Context(), reserved.Attempt.ExecutionID)
		require.ErrorIs(t, err, ErrCapability)
		_, err = reader.ReadCompletion(t.Context(), reserved.Attempt.ExecutionID)
		require.ErrorIs(t, err, ErrCapability)
		_, err = reader.ReadCompleted(t.Context(), executionattempt.Source{})
		require.ErrorIs(t, err, ErrCapability)
	}
	completion := Completion{Kind: "waza.execution-completion", SchemaVersion: Version,
		ExecutionID: reserved.Attempt.ExecutionID, ReservationDigest: reserved.Digest, ManifestDigest: strings.Repeat("a", 64)}
	wire, err := EncodeCompletion(completion)
	require.NoError(t, err)
	created, err := namespace.Create(t.Context(), "completion/"+reserved.Attempt.ExecutionID, append(bytes.Clone(wire), '\n'))
	require.NoError(t, err)
	require.True(t, created)
	record, err = reader.ReadCompletion(t.Context(), reserved.Attempt.ExecutionID)
	require.ErrorIs(t, err, ErrContext)
	require.Nil(t, record)
	require.Equal(t, fmt.Sprintf("%s: completion is not canonical or key-bound", ErrContext), err.Error())
}
