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

func TestReleaseResultRejectsPromotedAttemptKeyAliases(t *testing.T) {
	directory := authoredRootCollection(t, testPolicy(t, 8), true)
	raw, err := os.ReadFile(filepath.Join(directory, "baseline.result-binding.json"))
	require.NoError(t, err)
	expected, err := DecodeResult(raw)
	require.NoError(t, err)
	for _, field := range []string{"cluster_id", "task_id", "trial_ordinal"} {
		for _, order := range []string{"before", "after"} {
			t.Run(field+"/"+order, func(t *testing.T) {
				object, err := objectJSON(raw)
				require.NoError(t, err)
				attempts, ok := object["attempts"].([]any)
				require.True(t, ok)
				require.NotEmpty(t, attempts)
				attempt, ok := attempts[0].(map[string]any)
				require.True(t, ok)
				key, ok := attempt["key"].(map[string]any)
				require.True(t, ok)
				correct, err := json.Marshal(key[field])
				require.NoError(t, err)
				key[field] = "wrong"
				if field == "trial_ordinal" {
					key[field] = json.Number("999")
				}
				badKey, err := json.Marshal(key)
				require.NoError(t, err)
				canonicalValue, err := json.Marshal(key[field])
				require.NoError(t, err)
				canonical := `"` + field + `":` + string(canonicalValue)
				alias := `"` + strings.ToUpper(field) + `":` + string(correct)
				pair := alias + "," + canonical
				if order == "after" {
					pair = canonical + "," + alias
				}
				conflictingKey := bytes.Replace(badKey, []byte(canonical), []byte(pair), 1)
				modified, err := json.Marshal(object)
				require.NoError(t, err)
				modified = bytes.Replace(modified, badKey, conflictingKey, 1)
				if order == "after" {
					var legacy ResultBinding
					require.NoError(t, json.Unmarshal(modified, &legacy))
					require.Equal(t, expected.Attempts[0].Key, legacy.Attempts[0].Key,
						"typed decoding hides the conflicting original identity")
				}
				actual, err := DecodeResult(modified)
				require.ErrorContains(t, err, "exact spelling")
				require.Nil(t, actual)
			})
		}
	}
}
