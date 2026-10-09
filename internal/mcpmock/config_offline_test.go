package mcpmock

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestOfflineMCPMatcherBlocksDefaultFileLoaderWithoutChangingRuntime(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "valid-schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0600))
	fileURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(schemaPath)}).String()
	response := models.MCPMockResponse{MatchSchema: map[string]any{"$ref": fileURI}, Return: map[string]any{"state": "ready"}}
	mock := models.MCPMockConfig{Name: "offline", Tools: map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{response}}}}
	for _, useFixture := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "fixture"}[useFixture], func(t *testing.T) {
			selected := mock
			if useFixture {
				fixtures := filepath.Join(dir, "fixtures")
				require.NoError(t, os.Mkdir(fixtures, 0700))
				data, err := json.Marshal(Tool{Responses: convertResponses([]models.MCPMockResponse{response})})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(fixtures, "read.json"), data, 0600))
				selected.Tools, selected.Fixtures = nil, fixtures
			}
			_, err := FromEvalConfigOffline(selected, dir)
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, err, &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			_, err = FromEvalConfig(selected, dir)
			require.NoError(t, err, "legacy runtime matcher compilation keeps local-reference support")
		})
	}
}
