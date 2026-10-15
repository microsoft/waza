package releasepolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompleteTapeRejectsAliasesDespiteMatchingDigestAndTypedEvents(t *testing.T) {
	policy := testPolicy(t, 8)
	events := retryTestEvents(t, policy, "passed")
	firstAttempt := -1
	for i, event := range events {
		if event.Type == "attempt_start" {
			firstAttempt = i
			break
		}
	}
	require.NotEqual(t, -1, firstAttempt)
	for _, field := range []string{"cluster_id", "task_id", "trial_ordinal"} {
		t.Run(field, func(t *testing.T) {
			directory := authoredRootCollection(t, policy, true)
			control, err := ReadDecision(directory)
			require.NoError(t, err)
			require.True(t, control.Accepted)
			eventRaw, err := json.Marshal(events[firstAttempt])
			require.NoError(t, err)
			object, err := objectJSON(eventRaw)
			require.NoError(t, err)
			key, ok := object["key"].(map[string]any)
			require.True(t, ok)
			correct, err := json.Marshal(key[field])
			require.NoError(t, err)
			wrong := `"wrong"`
			if field == "trial_ordinal" {
				wrong = "999"
			}
			canonical := `"` + field + `":` + string(correct)
			aliased := bytes.Replace(eventRaw, []byte(canonical),
				[]byte(`"`+field+`":`+wrong+`,"`+strings.ToUpper(field)+`":`+string(correct)), 1)
			var decoded Event
			require.NoError(t, json.Unmarshal(aliased, &decoded))
			require.Equal(t, events[firstAttempt], decoded)
			stream := bytes.Replace(retryTestStream(t, events), eventRaw, aliased, 1)
			require.NotEqual(t, retryTestStream(t, events), stream)
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
			require.ErrorContains(t, err, "exact spelling")
			require.False(t, decision.Accepted)
		})
	}
}
