package releasepolicy

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseRequirednessCannotBeOverwrittenByAlias(t *testing.T) {
	policy := testPolicy(t, 8)
	for _, document := range []string{"policy", "planning input"} {
		for _, order := range []string{"before", "after"} {
			t.Run(document+"/"+order, func(t *testing.T) {
				var data []byte
				if document == "policy" {
					object, err := objectJSON(testBytes(t, policy))
					require.NoError(t, err)
					requirements, ok := object["requirements"].(map[string]any)
					require.True(t, ok)
					requirements["assurance"], requirements["Assurance"] = true, false
					data, err = SealJSON(object)
					require.NoError(t, err)
				} else {
					input, err := json.Marshal(policy.Requirements)
					require.NoError(t, err)
					object, err := objectJSON(input)
					require.NoError(t, err)
					object["assurance"], object["Assurance"] = true, false
					data, err = json.Marshal(object)
					require.NoError(t, err)
				}
				canonical, alias := `"assurance":true`, `"Assurance":false`
				withoutAlias := bytes.Replace(data, []byte(alias+","), nil, 1)
				require.NotEqual(t, data, withoutAlias)
				pair := alias + "," + canonical
				if order == "after" {
					pair = canonical + "," + alias
				}
				conflicting := bytes.Replace(withoutAlias, []byte(canonical), []byte(pair), 1)
				if document == "policy" {
					var legacy Policy
					require.NoError(t, json.Unmarshal(conflicting, &legacy))
					require.Equal(t, order == "before", legacy.Requirements.Assurance)
					actual, err := DecodePolicy(conflicting)
					require.ErrorContains(t, err, "exact spelling")
					require.Nil(t, actual)
				} else {
					_, err := DecodeRequirementsInput(conflicting)
					require.ErrorContains(t, err, "exact spelling")
				}
			})
		}
	}
}
