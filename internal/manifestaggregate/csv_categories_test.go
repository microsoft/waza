package manifestaggregate

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/stretchr/testify/require"
)

func TestAggregateCSVMissingSelectedSidecarRetainsEmptyCategoryArrays(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, _, _ := evidenceFixture(t, backend, 1, evidencestate.Unavailable, nil)
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, strings.Repeat("f", 64)))
	require.ErrorIs(t, err, ErrIncomplete)
	candidate := report.Evidence.Members[0].Candidates[0]
	require.Equal(t, "missing", candidate.State)
	require.Empty(t, candidate.Categories)
	require.Empty(t, candidate.Declarations)
	data, err := EncodeCSV(report)
	require.NoError(t, err)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Len(t, rows[1], 22)
	require.Equal(t, "category_evidence_json", rows[0][19])
	require.Equal(t, "declared_categories_json", rows[0][20])
	require.Equal(t, "[]", rows[1][19], "selected but unavailable is an empty category array, not an unselected blank")
	require.Equal(t, "[]", rows[1][20])
	require.Equal(t, "source_context_json", rows[0][21])
	require.Empty(t, rows[1][21], "unread sidecars cannot claim original source context")
}
