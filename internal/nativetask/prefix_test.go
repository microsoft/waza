package nativetask

import (
	"bytes"
	"context"
	"fmt"
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
		{name: "missing preparation", events: events, rows: rows, admissions: []*Admitted{{}}, invalid: true},
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
	full := &Admitted{prepared: &Prepared{data: make([]byte, assurance.MaxLabelBytes-prefixBindingOverhead)}}
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

func TestInspectPrefixRejectsOversizedAdmittedIdentityBeforeBinding(t *testing.T) {
	prepared, profile, original, _ := fixture(t)
	binding, err := original.Binding()
	require.NoError(t, err)
	for _, field := range []string{"cluster", "eval"} {
		t.Run(field, func(t *testing.T) {
			key := binding.Key
			oversized := strings.Repeat("a", 17<<20)
			if field == "cluster" {
				key.ClusterID = oversized
			} else {
				key.EvalID = oversized
			}
			admitted, err := Admit(t.Context(), prepared, profile, key)
			require.NoError(t, err, "primitive admission does not impose prefix inspection bounds")
			result, err := InspectPrefix(t.Context(), []byte("{}\n"), nil, []*Admitted{admitted})
			require.ErrorContains(t, err, "limit")
			require.NotContains(t, err.Error(), "inspecting native prefix", "reject before Binding or event decoding")
			require.Nil(t, result)
		})
	}
}

func TestInspectPrefixRetainedBindingBudget(t *testing.T) {
	prepared, profile, original, _ := fixture(t)
	binding, err := original.Binding()
	require.NoError(t, err)
	key := binding.Key
	key.ClusterID = strings.Repeat("<", assurance.MaxLabelBytes/2)
	key.EvalID = strings.Repeat("\x00", assurance.MaxLabelBytes/2)
	admitted, err := Admit(t.Context(), prepared, profile, key)
	require.NoError(t, err)
	require.Less(t, len(prepared.data), assurance.MaxLabelBytes)
	result, err := InspectPrefix(t.Context(), []byte("{}\n{}\n{}\n"), nil, []*Admitted{admitted, admitted, admitted})
	require.ErrorContains(t, err, "escaped byte limit")
	require.NotContains(t, err.Error(), "inspecting native prefix")
	require.Nil(t, result)
	allocations := testing.AllocsPerRun(3, func() {
		result, err := InspectPrefix(t.Context(), []byte("{}\n{}\n{}\n"), nil, []*Admitted{admitted, admitted, admitted})
		require.Error(t, err)
		require.Nil(t, result)
	})
	require.Less(t, allocations, 10.0, "preflight must not serialize caller-sized bindings")

	// Each entry charges a full MiB including the binding's fixed overhead.
	full := &Admitted{prepared: &Prepared{data: make([]byte, assurance.MaxLabelBytes-prefixBindingOverhead)}}
	admissions := make([]*Admitted, 16)
	for i := range admissions {
		admissions[i] = full
	}
	lines := bytes.Repeat([]byte("{}\n"), len(admissions))
	require.NoError(t, prefixInputs(t.Context(), lines, nil, admissions), "exact aggregate bound")
	last := *full
	last.binding.Key.ClusterID = "<"
	admissions[len(admissions)-1] = &last
	result, err = InspectPrefix(t.Context(), lines, nil, admissions)
	require.ErrorContains(t, err, "escaped byte limit", "escaped identity bytes count in the same aggregate")
	require.Nil(t, result)
	last.prepared = &Prepared{data: full.prepared.data[:len(full.prepared.data)-6]}
	require.NoError(t, prefixInputs(t.Context(), lines, nil, admissions), "exact conservative escaped bound")
	last.prepared = &Prepared{data: full.prepared.data[:len(full.prepared.data)-5]}
	require.Error(t, prefixInputs(t.Context(), lines, nil, admissions), "one encoded byte over aggregate bound")
}

func TestInspectPrefixBindingStringBoundsAndValidEscapes(t *testing.T) {
	for _, field := range []struct {
		name string
		text func(*Admission) *string
	}{
		{"collection", func(a *Admission) *string { return &a.CollectionID }},
		{"cluster", func(a *Admission) *string { return &a.Key.ClusterID }},
		{"task", func(a *Admission) *string { return &a.Key.TaskID }},
		{"eval", func(a *Admission) *string { return &a.Key.EvalID }},
		{"digest", func(a *Admission) *string { return &a.PolicyDigest.SHA256 }},
		{"encoding", func(a *Admission) *string { return &a.PolicyDigest.Encoding }},
	} {
		t.Run(field.name, func(t *testing.T) {
			admitted := &Admitted{prepared: &Prepared{}}
			*field.text(&admitted.binding) = strings.Repeat("<", assurance.MaxLabelBytes)
			require.NoError(t, prefixInputs(t.Context(), []byte("{}\n"), nil, []*Admitted{admitted}))
			*field.text(&admitted.binding) += "<"
			result, err := InspectPrefix(t.Context(), []byte("{}\n"), nil, []*Admitted{admitted})
			require.ErrorContains(t, err, "identity")
			require.Nil(t, result)
		})
	}
	prepared, profile, original, _ := fixture(t)
	binding, err := original.Binding()
	require.NoError(t, err)
	for _, id := range []string{"ordinary-id", "<>&\"\\\n\x00\u2028\u2029", strings.Repeat("x", 4096)} {
		key := binding.Key
		key.ClusterID, key.EvalID = id, id
		admitted, err := Admit(t.Context(), prepared, profile, key)
		require.NoError(t, err)
		start, err := admitted.Binding()
		require.NoError(t, err)
		events := append(encoded(t, Event{Kind: eventKind, Version: version, Sequence: 1, Type: "start", Admission: start}), '\n')
		result, err := InspectPrefix(t.Context(), events, nil, []*Admitted{admitted})
		require.NoError(t, err)
		require.Empty(t, result.Records)
		require.NotNil(t, result.Pending)
		require.Equal(t, key, result.Pending.Key)
	}
}

func TestInspectPrefixAdmittedIdentityTapeBoundary(t *testing.T) {
	prepared, profile, original, _ := fixture(t)
	binding, err := original.Binding()
	require.NoError(t, err)
	start := Event{Kind: eventKind, Version: version, Sequence: 1, Type: "start", Admission: binding}
	size := len(encoded(t, start)) + 1
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("input_limit%+d", delta), func(t *testing.T) {
			key := binding.Key
			key.ClusterID = strings.Repeat("x", assurance.MaxLabelBytes-size+len(key.ClusterID)+delta)
			admitted, err := Admit(t.Context(), prepared, profile, key)
			require.NoError(t, err)
			start.Admission, err = admitted.Binding()
			require.NoError(t, err)
			events := append(encoded(t, start), '\n')
			require.Len(t, events, assurance.MaxLabelBytes+delta)
			result, err := InspectPrefix(t.Context(), events, nil, []*Admitted{admitted})
			if delta > 0 {
				require.ErrorContains(t, err, "input exceeds")
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result.Pending)
			require.Equal(t, key, result.Pending.Key)
		})
	}
}
