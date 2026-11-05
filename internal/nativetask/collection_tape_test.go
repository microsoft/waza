//go:build darwin || linux

package nativetask

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func collectionDescriptorSources(t *testing.T, components int) []CapturedSource {
	t.Helper()
	// Canonical, POSIX-safe paths with components below 255 bytes and total
	// length within the public path bound. JSON HTML escaping expands them.
	directory := strings.Repeat(strings.Repeat("<", 200)+"/", components)
	sources := make([]CapturedSource, collectionSourceEntries)
	for i := range sources {
		sources[i] = CapturedSource{Path: fmt.Sprintf("%ssource-%05d.yaml", directory, i),
			Bytes: "", Digest: releasepolicy.SourceDigest(nil)}
		require.LessOrEqual(t, len(sources[i].Path), collectionPathBytes)
	}
	return sources
}

func collectionDescriptorProfile(t *testing.T, prepared *Prepared, record Record) *Profile {
	t.Helper()
	profile := &Profile{Kind: profileKind, Version: "2.0", Selection: Selection{"native_text_task", "2.0"},
		Nonce: strings.Repeat("a", 64), PermissionMode: "deny_all", PolicyDigest: record.PolicyDigest,
		Arms: map[releasepolicy.Arm]ArmPlan{}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		profile.Arms[arm] = ArmPlan{digest(t, string(arm)), []RequestPlan{{"task", prepared.RequestDigest()}}}
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = DecodeProfile(raw)
	require.NoError(t, err)
	return profile
}

func TestCollectionTapeIndependentDescriptorBudgets(t *testing.T) {
	_, _, req, _, record := collectionFixture(t)
	sources := collectionDescriptorSources(t, 4)
	prepared, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err)
	require.Greater(t, len(prepared.data), 64<<20)
	require.Less(t, len(prepared.data), collectionEncodedBytes)
	// Two independently allocated attempts exceed the old cumulative cap,
	// without large requests, sources, records or event streams.
	profile := collectionDescriptorProfile(t, prepared, record)
	admissions := make([]*Admitted, 2)
	rows := make([][]byte, len(admissions))
	for i := range admissions {
		key := record.Key
		key.Trial = i + 1
		admissions[i], err = Admit(t.Context(), prepared, profile, key)
		require.NoError(t, err)
		binding, err := admissions[i].Binding()
		require.NoError(t, err)
		row := record
		row.CollectionID, row.ProfileDigest, row.PlanDigest = binding.CollectionID, binding.ProfileDigest, binding.PlanDigest
		row.RequestDigest = binding.RequestDigest
		row.Key, row.Summary.Key = key, key
		row.Origin.RunNumber, row.ActualRow.Run.RunNumber = key.Trial, key.Trial
		row.ActualRow.Origin, row.Summary.Origin = row.Origin, row.Origin
		rows[i] = encoded(t, row)
		_, err = DecodeRecord(rows[i], admissions[i])
		require.NoError(t, err, "each record is independently admissible")
		require.NoError(t, MatchCollectionSnapshot(t.Context(), admissions[i], key, req, sources))
	}
	require.Greater(t, len(admissions)*len(prepared.data), collectionEncodedBytes)
	events, payload := fixtureTapeBytes(t, admissions, rows)
	require.Less(t, len(events), CollectionStreamBytes)
	require.Less(t, len(payload), CollectionStreamBytes)
	t.Logf("descriptor bytes=%d each, combined=%d; event bytes=%d; row bytes=%d",
		len(prepared.data), len(admissions)*len(prepared.data), len(events), len(payload))
	t.Run("reader", func(t *testing.T) {
		prefix, err := InspectCollectionPrefix(t.Context(), events, payload, admissions)
		require.NoError(t, err)
		require.Len(t, prefix.Records, len(admissions))
		require.Nil(t, prefix.Pending)
		pendingEvents := events[:bytes.LastIndexByte(events[:len(events)-1], '\n')+1]
		pendingRows := payload[:bytes.IndexByte(payload, '\n')+1]
		prefix, err = InspectCollectionPrefix(t.Context(), pendingEvents, pendingRows, admissions)
		require.NoError(t, err)
		require.Len(t, prefix.Records, 1)
		require.NotNil(t, prefix.Pending)
		require.Equal(t, admissions[1].binding, *prefix.Pending)
	})
	t.Run("writer", func(t *testing.T) {
		parent, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		defer func() { require.NoError(t, parent.Close()) }()
		tape, err := CreateCollectionTape(t.Context(), parent, "native")
		require.NoError(t, err)
		defer func() { require.NoError(t, tape.Close()) }()
		for i, admitted := range admissions {
			require.NoError(t, tape.Start(t.Context(), admitted), "attempt %d must have its own descriptor budget", i+1)
			require.NoError(t, tape.Complete(t.Context(), rows[i]))
		}
		actualEvents, err := tape.root.ReadFile(eventFile)
		require.NoError(t, err)
		actualRows, err := tape.root.ReadFile(payloadFile)
		require.NoError(t, err)
		require.Equal(t, events, actualEvents)
		require.Equal(t, payload, actualRows)
	})
}

func TestCollectionTapeStreamBoundaries(t *testing.T) {
	for _, delta := range []int{0, 1} {
		t.Run(string(rune('0'+delta)), func(t *testing.T) {
			_, admitted, _, _, record := collectionFixture(t)
			parent, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			defer func() { require.NoError(t, parent.Close()) }()
			tape, err := CreateCollectionTape(t.Context(), parent, "native")
			require.NoError(t, err)
			defer func() { require.NoError(t, tape.Close()) }()
			require.Equal(t, CollectionStreamBytes, tape.streamLimit)
			require.NoError(t, tape.Start(t.Context(), admitted))
			raw := encoded(t, record)
			raw = append(raw, bytes.Repeat([]byte{' '}, CollectionStreamBytes-1+delta-len(raw))...)
			require.Len(t, raw, CollectionStreamBytes-1+delta)
			err = tape.Complete(t.Context(), raw)
			payload, readErr := tape.root.ReadFile(payloadFile)
			require.NoError(t, readErr)
			events, readErr := tape.root.ReadFile(eventFile)
			require.NoError(t, readErr)
			if delta == 0 {
				require.NoError(t, err)
				require.Len(t, payload, CollectionStreamBytes, "newline is part of the exact public byte threshold")
				prefix, err := InspectCollectionPrefix(t.Context(), events, payload, []*Admitted{admitted})
				require.NoError(t, err)
				require.Len(t, prefix.Records, 1)
				_, err = InspectPrefix(t.Context(), events, payload, []*Admitted{admitted})
				require.ErrorContains(t, err, "1 MiB")
			} else {
				require.ErrorContains(t, err, "stream exceeds")
				require.Empty(t, payload, "no payload bytes may be appended after a capacity failure")
				require.Equal(t, 1, bytes.Count(events, []byte{'\n'}), "only the durable start remains")
				require.True(t, tape.poisoned)
				require.Error(t, tape.Start(t.Context(), admitted))
				prefix, err := InspectCollectionPrefix(t.Context(), events, payload, []*Admitted{admitted})
				require.NoError(t, err)
				require.NotNil(t, prefix.Pending)
			}
		})
	}
}

func TestCollectionTapeChecksEventCapacityBeforeAnyCompletionWrite(t *testing.T) {
	for _, phase := range []string{"start", "terminal"} {
		for _, delta := range []int{0, 1} {
			t.Run(phase+string(rune('0'+delta)), func(t *testing.T) {
				_, admitted, _, _, record := collectionFixture(t)
				parent, err := os.OpenRoot(t.TempDir())
				require.NoError(t, err)
				defer func() { require.NoError(t, parent.Close()) }()
				tape, err := CreateCollectionTape(t.Context(), parent, "native")
				require.NoError(t, err)
				defer func() { require.NoError(t, tape.Close()) }()
				binding, err := admitted.Binding()
				require.NoError(t, err)
				event := Event{Type: "start", Admission: binding}
				raw := encoded(t, record)
				if phase == "terminal" {
					require.NoError(t, tape.Start(t.Context(), admitted))
					digest, row, err := recordDigests(raw)
					require.NoError(t, err)
					event = Event{Type: "terminal", Admission: binding, State: record.Summary.Category, RecordDigest: digest, RowDigest: row}
				}
				data, err := tape.eventData(event)
				require.NoError(t, err)
				tape.eventBytes = CollectionStreamBytes - len(data) + delta
				before, err := tape.root.ReadFile(eventFile)
				require.NoError(t, err)
				if phase == "start" {
					err = tape.Start(t.Context(), admitted)
				} else {
					err = tape.Complete(t.Context(), raw)
				}
				after, readErr := tape.root.ReadFile(eventFile)
				require.NoError(t, readErr)
				payload, readErr := tape.root.ReadFile(payloadFile)
				require.NoError(t, readErr)
				if delta == 0 {
					require.NoError(t, err)
					require.Equal(t, CollectionStreamBytes, tape.eventBytes)
					require.Equal(t, append(before, data...), after)
				} else {
					require.ErrorContains(t, err, "stream exceeds")
					require.Equal(t, before, after)
					require.Empty(t, payload, "terminal capacity is checked before writing a payload")
					require.True(t, tape.poisoned)
				}
			})
		}
	}

}

func TestCollectionDescriptorExactBoundary(t *testing.T) {
	_, _, req, _, record := collectionFixture(t)
	sources := collectionDescriptorSources(t, 6)
	prepared, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err)
	padding := collectionEncodedBytes - len(prepared.data)
	require.Positive(t, padding)
	require.Less(t, padding+len(req.Message), CollectionStreamBytes)
	req.Message += strings.Repeat("x", padding)
	prepared, err = PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err)
	require.Len(t, prepared.data, 128<<20, "the actual production descriptor threshold, not a scaled budget")
	profile := collectionDescriptorProfile(t, prepared, record)
	admitted, err := Admit(t.Context(), prepared, profile, record.Key)
	require.NoError(t, err)
	parent, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	tape, err := CreateCollectionTape(t.Context(), parent, "native")
	require.NoError(t, err)
	defer func() { require.NoError(t, tape.Close()) }()
	require.NoError(t, tape.Start(t.Context(), admitted), "bindings must not reduce the descriptor's own allowance")
	events, err := tape.root.ReadFile(eventFile)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(events, []byte{'\n'}))
	payload, err := tape.root.ReadFile(payloadFile)
	require.NoError(t, err)
	require.Empty(t, payload)
	prefix, err := InspectCollectionPrefix(t.Context(), events, payload, []*Admitted{admitted})
	require.NoError(t, err)
	require.Empty(t, prefix.Records)
	require.NotNil(t, prefix.Pending)
	require.Equal(t, admitted.binding, *prefix.Pending)

	req.Message += "x"
	oversized, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.EqualError(t, err, "native encoded inventory exceeds bounded input")
	require.Nil(t, oversized)
	_, resourceBytes, err := collectionRequest(req)
	require.NoError(t, err, "the oversized descriptor still has an admissible request")
	require.Zero(t, resourceBytes, "the request has no resource or instruction content")
}

func TestCollectionTapeRejectsPrivatePreparationBeforeStart(t *testing.T) {
	_, _, admitted, _ := fixture(t)
	parent, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	tape, err := CreateCollectionTape(t.Context(), parent, "native")
	require.NoError(t, err)
	defer func() { require.NoError(t, tape.Close()) }()
	require.EqualError(t, tape.Start(t.Context(), admitted), "native collection prefix requires public 2.0 preparation")
	events, err := tape.root.ReadFile(eventFile)
	require.NoError(t, err)
	require.Empty(t, events)
	require.True(t, tape.poisoned)
	_, public, _, _, _ := collectionFixture(t)
	require.EqualError(t, tape.Start(t.Context(), public), "native tape cannot admit another start")
}
