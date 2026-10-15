//go:build darwin || linux

package nativetask

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTapeDurableDigestsAndUniqueAllocation(t *testing.T) {
	_, _, admitted, record := fixture(t)
	original := encoded(t, record)
	modified := bytes.Replace(original, []byte(`"score":1,`), []byte(`"score":1.0,`), 1)
	require.NotEqual(t, original, modified)
	parent, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	tape, err := CreateTape(context.Background(), parent, "collection")
	require.NoError(t, err)
	require.NoError(t, tape.Start(context.Background(), admitted))
	require.NoError(t, tape.Complete(context.Background(), modified))
	require.Error(t, tape.Start(context.Background(), admitted))
	prefix, err := ReadPrefix(context.Background(), tape.root, []*Admitted{admitted})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 1)
	events, err := tape.root.ReadFile(eventFile)
	require.NoError(t, err)
	payload, err := tape.root.ReadFile(payloadFile)
	require.NoError(t, err)
	expectedEvents, expectedPayload := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{modified})
	require.Equal(t, expectedEvents, events)
	require.Equal(t, expectedPayload, payload)
	require.NoError(t, tape.Close())
}

func TestTapeDurableBindsMultipleAttemptsIncludingOperationalRows(t *testing.T) {
	prepared, profile, first, r1 := fixture(t)
	key := r1.Key
	key.Attempt = 2
	second, err := Admit(context.Background(), prepared, profile, key)
	require.NoError(t, err)
	r2 := operational(r1, "Synthetic second attempt failed grading.")
	r2.Key, r2.Summary.Key = key, key
	r2.Origin.AttemptCount = 2
	r2.ActualRow.Origin, r2.Summary.Origin = r2.Origin, r2.Origin
	r2.ActualRow.Run.Attempts = 2
	r2.Lifecycle.Grade = Phase{"failed", "grade_failed"}
	parent, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	tape, err := CreateTape(context.Background(), parent, "collection")
	require.NoError(t, err)
	defer func() { require.NoError(t, tape.Close()) }()
	require.NoError(t, tape.Start(context.Background(), first))
	require.NoError(t, tape.Complete(context.Background(), encoded(t, r1)))
	require.NoError(t, tape.Start(context.Background(), second))
	require.NoError(t, tape.Complete(context.Background(), encoded(t, r2)))
	events, err := tape.root.ReadFile(eventFile)
	require.NoError(t, err)
	payload, err := tape.root.ReadFile(payloadFile)
	require.NoError(t, err)
	expectedEvents, expectedPayload := fixtureTapeBytes(t, []*Admitted{first, second}, [][]byte{encoded(t, r1), encoded(t, r2)})
	require.Equal(t, expectedEvents, events)
	require.Equal(t, expectedPayload, payload)
	prefix, err := ReadPrefix(context.Background(), tape.root, []*Admitted{first, second})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 2)
	require.Equal(t, "operational", prefix.Records[1].Summary.Category)
}

func TestTapeDurablePayloadBeforeTerminalAndCrashPrefixes(t *testing.T) {
	for _, failure := range []string{"none", "payload sync", "terminal sync", "start only", "orphan", "null start field", "torn", "duplicate payload", "changed payload", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			_, _, admitted, r := fixture(t)
			parent, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			defer func() { require.NoError(t, parent.Close()) }()
			tape, err := CreateTape(context.Background(), parent, "collection")
			require.NoError(t, err)
			require.NoError(t, tape.Start(context.Background(), admitted))
			failed := errors.New("injected fsync failure")
			switch failure {
			case "payload sync":
				tape.syncPayload = func() error { return failed }
			case "terminal sync":
				tape.syncEvents = func() error { return failed }
			}
			if failure != "start only" {
				ctx := context.Background()
				if failure == "cancel" {
					c, cancel := context.WithCancel(ctx)
					cancel()
					ctx = c
				}
				err = tape.Complete(ctx, encoded(t, r))
				if failure == "payload sync" || failure == "terminal sync" || failure == "cancel" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
			if tape.poisoned {
				require.Error(t, tape.Start(context.Background(), admitted))
			}
			events, err := tape.root.ReadFile(eventFile)
			require.NoError(t, err)
			payload, err := tape.root.ReadFile(payloadFile)
			require.NoError(t, err)
			switch failure {
			case "orphan":
				events = bytes.SplitAfter(events, []byte{'\n'})[0]
			case "null start field":
				events = bytes.Replace(events, []byte(`"type":"start"`), []byte(`"type":"start","row_digest":null`), 1)
			case "torn":
				events = events[:len(events)-1]
			case "duplicate payload":
				payload = append(payload, bytes.Clone(payload)...)
			case "changed payload":
				payload = bytes.Replace(payload, []byte("actual nonempty"), []byte("tampered nonempty"), 1)
			}
			prefix, err := verifyPrefix(context.Background(), events, payload, []*Admitted{admitted})
			switch failure {
			case "none", "terminal sync":
				require.NoError(t, err)
				require.Len(t, prefix.Records, 1)
				require.Nil(t, prefix.Pending)
			case "start only", "cancel":
				require.NoError(t, err)
				require.NotNil(t, prefix.Pending)
			default:
				require.Error(t, err)
			}
			if failure == "payload sync" {
				require.NotContains(t, string(events), `"type":"terminal"`)
			}
			require.NoError(t, tape.Close())
			_, err = CreateTape(context.Background(), parent, "collection")
			require.Error(t, err, "never reuse or repair a reserved destination")
		})
	}
}
