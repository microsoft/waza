package webapi

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestMapSessionDigestToolPolicy(t *testing.T) {
	for _, mode := range []string{"unrestricted", "deny_all", "allow_list"} {
		t.Run(mode, func(t *testing.T) {
			digest := &models.SessionDigest{
				ToolPolicyMode:    mode,
				ToolPolicyDenials: []models.ToolPolicyDenial{{Tool: "bash", Kind: "shell", Reason: "undeclared"}},
			}
			mapped := mapSessionDigest(digest)
			require.Equal(t, mode, mapped.ToolPolicyMode)
			require.Equal(t, digest.ToolPolicyDenials, mapped.ToolPolicyDenials)
			data, err := json.Marshal(mapped)
			require.NoError(t, err)
			require.Contains(t, string(data), `"toolPolicyMode":"`+mode+`"`)
			require.Contains(t, string(data), `"toolPolicyDenials":[{"tool":"bash","kind":"shell","reason":"undeclared"}]`)
		})
	}
}
