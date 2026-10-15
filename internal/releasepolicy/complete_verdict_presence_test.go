package releasepolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompleteTapeRequiresExplicitFailedVerdicts(t *testing.T) {
	policy := testPolicy(t, 8)
	events := retryTestEvents(t, policy, "failed")
	terminal := -1
	for i, event := range events {
		if event.Type == "attempt_terminal" {
			terminal = i
			break
		}
	}
	require.NotEqual(t, -1, terminal)
	require.False(t, events[terminal].Attempt.Checks[0].Passed)
	for _, mutation := range []string{"missing", "null"} {
		t.Run(mutation, func(t *testing.T) {
			directory := authoredRootCollectionWithStatus(t, policy, true, "failed")
			_, err := ReadDecision(directory)
			require.NoError(t, err, "explicit false is a valid failed observation")
			eventRaw, err := json.Marshal(events[terminal])
			require.NoError(t, err)
			replacement := `"passed":null,`
			if mutation == "missing" {
				replacement = ""
			}
			modified := bytes.Replace(eventRaw, []byte(`"passed":false,`), []byte(replacement), 1)
			require.NotEqual(t, eventRaw, modified)
			var legacy Event
			require.NoError(t, json.Unmarshal(modified, &legacy))
			require.Equal(t, events[terminal], legacy)
			stream := bytes.Replace(retryTestStream(t, events), eventRaw, modified, 1)
			journalRaw := retryTestJournal(t, policy, events, stream)
			journal, err := DecodeJournal(journalRaw, policy)
			require.NoError(t, err)
			require.Equal(t, SourceDigest(stream), journal.StreamDigest)
			receipts, err := Replay(policy, journal)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "journal.ndjson"), stream, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "journal.json"), journalRaw, 0o600))
			for _, arm := range []Arm{Baseline, Candidate} {
				final, err := SealJSON(receipts[arm])
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(directory, string(arm)+".final.json"), final, 0o600))
			}
			decision, err := ReadDecision(directory)
			require.Error(t, err, "missing or null raw verdict must not become an explicit false")
			require.Equal(t, "invalid", decision.Compatibility.State)
			require.False(t, decision.Accepted)
		})
	}
}
