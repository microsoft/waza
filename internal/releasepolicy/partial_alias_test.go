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

func TestPartialReadersRejectPromotedIdentityAliases(t *testing.T) {
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
	good := retryTestStream(t, events[:firstAttempt+1])
	_, err := InspectRetryPrefix(t.Context(), testBytes(t, policy), good)
	require.NoError(t, err)
	for _, field := range []string{"cluster_id", "task_id", "trial_ordinal"} {
		for _, order := range []string{"before", "after"} {
			t.Run(field+"/"+order, func(t *testing.T) {
				eventRaw, err := json.Marshal(events[firstAttempt])
				require.NoError(t, err)
				object, err := objectJSON(eventRaw)
				require.NoError(t, err)
				key, ok := object["key"].(map[string]any)
				require.True(t, ok)
				value, err := json.Marshal(key[field])
				require.NoError(t, err)
				canonical := `"` + field + `":` + string(value)
				alias := `"` + strings.ToUpper(field) + `":` + string(value)
				pair := alias + "," + canonical
				if order == "after" {
					pair = canonical + "," + alias
				}
				require.Contains(t, string(eventRaw), canonical)
				aliased := bytes.Replace(eventRaw, []byte(canonical), []byte(pair), 1)
				stream := append(retryTestStream(t, events[:firstAttempt]), aliased...)
				stream = append(stream, '\n')
				prefix, err := InspectRetryPrefix(t.Context(), testBytes(t, policy), stream)
				require.ErrorContains(t, err, "exact spelling")
				require.Nil(t, prefix)
				directory := authoredRootCollection(t, policy, false)
				require.NoError(t, os.WriteFile(filepath.Join(directory, "journal.ndjson"), stream, 0o600))
				decision, err := ReadDecision(directory)
				require.ErrorContains(t, err, "exact spelling")
				require.False(t, decision.Accepted)
			})
		}
	}
}
