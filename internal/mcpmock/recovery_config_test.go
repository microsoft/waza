package mcpmock

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLegacyResolutionDoesNotEraseCallerSequences(t *testing.T) {
	mock := models.MCPMockConfig{Name: "dependency", Tools: map[string]models.MCPMockTool{
		"read": {Responses: []models.MCPMockResponse{{Sequence: []models.MCPMockStep{{Error: "temporary"}, {Return: "ready"}}}}},
	}}
	for _, resolve := range []func(models.MCPMockConfig, string) (*Config, error){FromEvalConfig, FromEvalConfigOffline} {
		legacy, err := resolve(mock, "")
		require.NoError(t, err)
		require.Empty(t, legacy.Tools["read"].Responses[0].Sequence)
		require.Len(t, mock.Tools["read"].Responses[0].Sequence, 2)
		got, err := FromEvalConfigWithVersion(mock, "", models.ScenarioSchemaVersion)
		require.NoError(t, err)
		requireToolError(t, NewServer(got, nil), "read", nil, "temporary")
	}
}

func TestVersionedResolutionAppliesInlineOverrideBeforeSequenceAdmission(t *testing.T) {
	dir := t.TempDir()
	writeSourceFixture(t, dir, "read.json", `{"responses":[{"sequence":[]}]}`)
	mock := models.MCPMockConfig{Name: "dependency", Fixtures: dir, Tools: map[string]models.MCPMockTool{
		"read": {Responses: []models.MCPMockResponse{{Return: "inline"}}},
	}}
	for _, resolve := range []func(models.MCPMockConfig, string, string) (*Config, error){FromEvalConfigWithVersion, FromEvalConfigOfflineWithVersion} {
		got, err := resolve(mock, "", "1.4")
		require.NoError(t, err)
		requireToolJSON(t, NewServer(got, nil), "read", nil, "inline")
	}
}
