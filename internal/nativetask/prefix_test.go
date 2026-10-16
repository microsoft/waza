package nativetask

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/stretchr/testify/require"
)

func TestInspectPrefixPreservesStrictTapeAdmission(t *testing.T) {
	_, _, admitted, record := fixture(t)
	events, rows := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{encoded(t, record)})
	for _, tc := range []struct {
		name       string
		events     []byte
		rows       []byte
		admissions []*Admitted
		records    int
		pending    bool
		invalid    bool
	}{
		{name: "missing"},
		{name: "empty", events: []byte{}, rows: []byte{}},
		{name: "terminal", events: events, rows: rows, admissions: []*Admitted{admitted}, records: 1},
		{name: "pending", events: bytes.SplitAfterN(events, []byte{'\n'}, 2)[0], admissions: []*Admitted{admitted}, pending: true},
		{name: "torn events", events: events[:len(events)-1], rows: rows, admissions: []*Admitted{admitted}, invalid: true},
		{name: "torn rows", events: events, rows: rows[:len(rows)-1], admissions: []*Admitted{admitted}, invalid: true},
		{name: "orphan", events: bytes.SplitAfterN(events, []byte{'\n'}, 2)[0], rows: rows, admissions: []*Admitted{admitted}, invalid: true},
		{name: "late invalid event", events: append(bytes.Clone(events), []byte("{}\n")...), rows: rows, admissions: []*Admitted{admitted}, invalid: true},
		{name: "unused admission", events: []byte{}, admissions: []*Admitted{admitted}, invalid: true},
		{name: "missing admission", events: events, rows: rows, invalid: true},
		{name: "nil admission", events: events, rows: rows, admissions: []*Admitted{nil}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := InspectPrefix(t.Context(), tc.events, tc.rows, tc.admissions)
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, result, "invalid input must discard even an otherwise valid earlier record")
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Records, tc.records)
			require.Equal(t, tc.pending, result.Pending != nil)
		})
	}
	result, err := InspectPrefix(t.Context(), events, rows, []*Admitted{admitted})
	require.NoError(t, err)
	result.Records[0].ActualRow.Run.Validations["changed"] = result.Records[0].ActualRow.Run.Validations["check"]
	result, err = InspectPrefix(t.Context(), events, rows, []*Admitted{admitted})
	require.NoError(t, err)
	require.NotContains(t, result.Records[0].ActualRow.Run.Validations, "changed")
}

func TestInspectPrefixBoundsAndCancellation(t *testing.T) {
	_, _, admitted, record := fixture(t)
	events, rows := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{encoded(t, record)})
	for _, input := range []struct {
		name string
		data []byte
	}{
		{"byte limit", bytes.Repeat([]byte{' '}, assurance.MaxLabelBytes+1)},
		{"line limit", bytes.Repeat([]byte{'\n'}, maxPrefixEvents+1)},
	} {
		t.Run(input.name, func(t *testing.T) {
			require.Error(t, prefixInputs(t.Context(), input.data, nil, nil))
			require.Error(t, prefixInputs(t.Context(), nil, input.data, nil))
		})
	}
	require.NoError(t, prefixInputs(t.Context(), bytes.Repeat([]byte{' '}, assurance.MaxLabelBytes), nil, nil))
	require.NoError(t, prefixInputs(t.Context(), bytes.Repeat([]byte{'\n'}, maxPrefixEvents), nil, nil))
	oversized := &Admitted{prepared: &Prepared{data: []byte(strings.Repeat("a", assurance.MaxLabelBytes+1))}}
	require.Error(t, prefixInputs(t.Context(), events, rows, []*Admitted{oversized}))
	full := &Admitted{prepared: &Prepared{data: make([]byte, assurance.MaxLabelBytes)}}
	admissions := make([]*Admitted, maxPrefixAdmissionBytes/assurance.MaxLabelBytes)
	for i := range admissions {
		admissions[i] = full
	}
	lines := bytes.Repeat([]byte{'\n'}, len(admissions)+1)
	require.NoError(t, prefixInputs(t.Context(), lines, nil, admissions))
	require.Error(t, prefixInputs(t.Context(), lines, nil, append(admissions, full)))
	result, err := InspectPrefix(nil, events, rows, []*Admitted{admitted}) //nolint:staticcheck // Explicit nil-context boundary.
	require.Error(t, err)
	require.Nil(t, result)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = InspectPrefix(ctx, events, rows, []*Admitted{admitted})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
}
