package manifestaggregate

import (
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/stretchr/testify/require"
)

func TestEvidenceReaderRejectsMissingInvocation(t *testing.T) {
	_, err := NewEvidenceReader(nil)
	require.ErrorIs(t, err, ErrInvalid)
	for _, reader := range []*EvidenceReader{nil, {}} {
		_, _, err := reader.InspectCategories(t.Context(), "")
		require.ErrorIs(t, err, ErrInvalid)
		source, err := reader.InspectSourceContext(t.Context(), evidencestate.Document{})
		require.ErrorIs(t, err, ErrInvalid)
		require.Nil(t, source)
	}
}

func TestEvidenceReaderDoesNotInventUndeclaredSource(t *testing.T) {
	reader, err := NewEvidenceReader(&memoryBackend{})
	require.NoError(t, err)
	source, err := reader.InspectSourceContext(t.Context(), evidencestate.Document{})
	require.NoError(t, err)
	require.Equal(t, "unavailable", source.State)
	require.Equal(t, "unverified", source.Verification)
	require.Nil(t, source.Source)
	require.Zero(t, reader.backend.read)
}
