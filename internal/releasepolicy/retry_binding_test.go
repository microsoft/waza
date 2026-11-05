package releasepolicy

import (
	"bytes"
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRetryViewForPolicyBindsOriginalBytesAndWholePrefix(t *testing.T) {
	p := testPolicy(t, 1)
	raw := testBytes(t, p)
	events := retryTestEvents(t, p, "passed")
	stream := retryTestStream(t, events[:4])
	prefix, err := InspectRetryPrefix(t.Context(), raw, stream)
	require.NoError(t, err)
	view, seal, err := prefix.ViewForPolicy(t.Context(), raw)
	require.NoError(t, err)
	require.Equal(t, prefix.seal, seal)
	require.Len(t, view.Events, 4)
	view.Events[0].EvalID = "caller mutation"
	view, _, err = prefix.ViewForPolicy(t.Context(), raw)
	require.NoError(t, err)
	require.NotEqual(t, "caller mutation", view.Events[0].EvalID)
	_, changed, err := prefix.ViewForPolicy(t.Context(), append(bytes.Clone(raw), ' '))
	require.Error(t, err)
	require.Equal(t, models.EvidenceDigest{}, changed)
	torn, err := InspectRetryPrefix(t.Context(), raw, append(bytes.Clone(stream), 'x'))
	require.NoError(t, err)
	_, tornSeal, err := torn.ViewForPolicy(t.Context(), raw)
	require.NoError(t, err)
	require.NotEqual(t, seal, tornSeal, "even an unparsed fragment belongs to the retained binding")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = prefix.ViewForPolicy(ctx, raw)
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = prefix.ViewForPolicy(nil, raw) //nolint:staticcheck // Explicit nil-context boundary.
	require.Error(t, err)
	_, _, err = (*RetryPrefix)(nil).ViewForPolicy(t.Context(), raw)
	require.Error(t, err)
	prefix.rawStream[0] = 'x'
	_, _, err = prefix.ViewForPolicy(t.Context(), raw)
	require.Error(t, err)
}
