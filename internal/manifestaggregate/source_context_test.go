package manifestaggregate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func TestAggregateSourceContextUsesActualNamespaceAndStoreBudget(t *testing.T) {
	directory := t.TempDir()
	local, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, local.Close()) }()
	manifest, terminal := fixture(t, 1, 1, runnerterminal.Success)
	ledger, err := executionledger.New(local)
	require.NoError(t, err)
	creator, err := ledger.Reserve(t.Context(), manifest.Binding, nil)
	require.NoError(t, err)
	reservation, err := ledger.Reservation(creator)
	require.NoError(t, err)
	const name = "execution-attempt.json"
	manifest.Artifacts = append(manifest.Artifacts, artifactmanifest.Artifact{
		Name: name, MediaType: "application/json", SHA256: reservation.Digest, SizeBytes: int64(len(reservation.Bytes)),
	})
	store := artifactmanifest.NewStore(local)
	for _, artifact := range manifest.Artifacts {
		data := terminal
		if artifact.Name == name {
			data = reservation.Bytes
		}
		require.NoError(t, store.Put(t.Context(), artifact, data))
	}
	wire, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	digest, err := store.Create(t.Context(), wire)
	require.NoError(t, err)
	_, err = store.Finalize(t.Context(), digest)
	require.NoError(t, err)
	completed, err := ledger.Complete(t.Context(), creator, digest, name)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: reservation.Attempt.ExecutionID, ManifestDigest: digest}
	document := evidencestate.Document{ExecutionSource: &source, AttemptArtifact: name}
	budget := &readBudget{}
	backend := &boundedBackend{Backend: local, readBudget: budget}
	context, err := inspectSourceContext(t.Context(), backend, document)
	require.NoError(t, err)
	require.Equal(t, "verified", context.State)
	require.Greater(t, budget.read, int64(len(reservation.Bytes)+len(completed.CompletionBytes)+len(wire)),
		"the SAME budget includes actual namespace records and Store manifest/artifacts/marker")

	budget.read = MaxReadBytes - artifactmanifest.MaxNamespaceObjectBytes - 1
	context, err = inspectSourceContext(t.Context(), backend, document)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, "limit_exceeded", context.State)
	require.EqualValues(t, MaxReadBytes-artifactmanifest.MaxNamespaceObjectBytes-1+len(reservation.Bytes), budget.read,
		"reservation succeeds/refunds, then completion is denied before its raw read")
	require.Zero(t, budget.unavailable)

	path := filepath.Join(directory, artifactmanifest.ExecutionLedgerNamespaceV1+"-reservation-"+source.ExecutionID)
	require.FileExists(t, path)
	require.NoError(t, os.Remove(path))
	budget.read = MaxReadBytes - artifactmanifest.MaxNamespaceObjectBytes - 1
	context, err = inspectSourceContext(t.Context(), backend, document)
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	require.Equal(t, "missing", context.State)
	require.EqualValues(t, MaxReadBytes, budget.read, "failed namespace I/O retains its entire limit+1 reservation")
	context, err = inspectSourceContext(t.Context(), backend, document)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, "limit_exceeded", context.State, "no budget-free missing-source retry")
}

func TestAggregateSourceContextCannotAdaptDecoratorsOrInventSources(t *testing.T) {
	budget := &readBudget{}
	backend := &boundedBackend{Backend: &memoryBackend{}, readBudget: budget}
	context, err := inspectSourceContext(t.Context(), backend, evidencestate.Document{})
	require.NoError(t, err)
	require.Equal(t, "unavailable", context.State)
	require.Nil(t, context.Source)
	require.Zero(t, budget.read, "no source declaration means no namespace admission or I/O")
	context, err = inspectSourceContext(t.Context(), backend, evidencestate.Document{
		ExecutionSource: &executionattempt.Source{ExecutionID: "exec-" + strings.Repeat("a", 64), ManifestDigest: strings.Repeat("b", 64)},
		AttemptArtifact: "execution-attempt.json",
	})
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, err, artifactmanifest.ErrUnsupportedBackend)
	require.Equal(t, "unavailable", context.State)
	require.Zero(t, budget.read, "unsupported providers are not unwrapped or replaced")
}
