package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMCPMockResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		response MCPMockResponse
		want     string
	}{
		{"wildcard", MCPMockResponse{}, ""},
		{"regex", MCPMockResponse{MatchRegex: map[string]string{"id": "^item$"}}, ""},
		{"invalid regex", MCPMockResponse{MatchRegex: map[string]string{"id": "["}}, "invalid regex"},
		{"schema", MCPMockResponse{MatchSchema: map[string]any{"type": "object"}}, ""},
		{"invalid schema", MCPMockResponse{MatchSchema: map[string]any{"type": 42}}, "match_schema is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMCPMockResponse(test.response)
			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}
		})
	}
}
