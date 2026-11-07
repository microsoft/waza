package manifestaggregate

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAggregateEvidenceUnindexedCSVWithholdsIntegerAndCredits(t *testing.T) {
	for _, integer := range []string{"9007199254740993", "9223372036854775807"} {
		for _, credits := range []*float64{nil, new(float64)} {
			t.Run(integer+"/"+strconv.FormatBool(credits != nil), func(t *testing.T) {
				tokens, err := strconv.Atoi(integer)
				require.NoError(t, err)
				payload, err := json.Marshal(models.UsageStats{
					InputTokens: tokens, AICredits: credits,
					ModelMetrics: map[string]models.ModelUsage{"recorded-model": {InputTokens: tokens, AICredits: credits}},
				})
				require.NoError(t, err)
				backend := &memoryBackend{objects: map[string][]byte{}}
				origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Present, payload)
				report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
				require.NoError(t, err)
				raw, err := Encode(report)
				require.NoError(t, err)
				require.NotContains(t, string(raw), `"input_tokens"`)
				data, err := EncodeCSV(report)
				require.NoError(t, err)
				rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
				require.NoError(t, err)
				require.Len(t, rows, 2)
				require.Len(t, rows[1], 22)
				require.Equal(t, `{"state":"unavailable","verification":"unverified"}`, rows[1][21])
				require.Empty(t, rows[1][9])
				require.Empty(t, rows[1][15])
				require.Equal(t, "withheld", rows[1][16])
				require.Equal(t, "unavailable", rows[1][17])
				var scope []string
				require.NoError(t, json.Unmarshal([]byte(rows[1][18]), &scope))
				require.Equal(t, []string{origin}, scope)
				require.Empty(t, rows[1][14])
				require.Equal(t, "unavailable", rows[1][5])
			})
		}
	}
}

func TestAggregateCSVUnavailableAndOutputBounds(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Present, nil)
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
	require.NoError(t, err)
	data, err := EncodeCSV(report)
	require.NoError(t, err)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, field := range rows[1][8:16] {
		require.Empty(t, field, "declared present-empty does not synthesize numeric zero")
	}
	_, err = EncodeCSV(Report{})
	require.Error(t, err)
	var writer boundedCSV
	count, err := writer.Write(make([]byte, MaxReportBytes))
	require.NoError(t, err)
	require.Equal(t, MaxReportBytes, count)
	count, err = writer.Write([]byte{'\n'})
	require.ErrorIs(t, err, ErrLimit)
	require.Zero(t, count)
	require.Equal(t, MaxReportBytes, writer.Len())
	writer = boundedCSV{}
	encoder := csv.NewWriter(&writer)
	writeErr := encoder.Write([]string{strings.Repeat("a", MaxReportBytes+1)})
	encoder.Flush()
	require.ErrorIs(t, errors.Join(writeErr, encoder.Error()), ErrLimit, "buffered string writes must not bypass the cap")
}

func TestAggregateCSVPreservesVerifiedLineageScope(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, _, document := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, new(float64)))
	ancestor, _, _ := evidenceFixture(t, backend, 2, evidencestate.Unavailable, nil)
	document.Lineage = []evidencestate.Lineage{{Manifest: ancestor, Relationship: evidencestate.Aggregation}}
	inspection, err := evidencestate.Publish(t.Context(), backend, document)
	require.NoError(t, err)
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, inspection.EvidenceSHA256))
	require.NoError(t, err)
	data, err := EncodeCSV(report)
	require.NoError(t, err)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	var sources []string
	require.NoError(t, json.Unmarshal([]byte(rows[1][18]), &sources))
	expected := []string{origin, ancestor}
	slices.Sort(expected)
	require.Equal(t, expected, sources)
	require.Equal(t, "authored", rows[1][7], "declared kind remains separate from verified source scope")
}
