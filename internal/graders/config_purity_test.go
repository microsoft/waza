package graders

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestValidateConfigExternalSchemasHaveNoDefaultLoader(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		if err != nil {
			t.Errorf("writing sentinel schema: %v", err)
		}

	}))
	defer server.Close()
	dir := t.TempDir()
	local := filepath.Join(dir, "readable-valid-schema.json")
	require.NoError(t, os.WriteFile(local, []byte(`{"type":"object"}`), 0600))
	fileURI := testutil.FileURL(local)
	for _, target := range []string{server.URL + "/schema", fileURI} {
		t.Run(target, func(t *testing.T) {
			params := models.JSONSchemaGraderParameters{Schema: map[string]any{"$ref": target}}
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			_, err := Create("legacy", params)
			require.NoError(t, err, "runtime constructor keeps its deferred schema behavior")
			require.Zero(t, requests.Load(), "no HTTP schema fetch, including an available valid target")
		})
	}
	require.NoError(t, os.Remove(local))
	params := models.JSONSchemaGraderParameters{Schema: map[string]any{"$ref": fileURI}}
	var loadErr *jsonschema.LoadURLError
	require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
	require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference, "file existence must not affect pure external-reference resolution")
}

func TestValidateConfigArgumentSchemasHaveNoDefaultLoader(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	local := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(local, []byte(`{"type":"object"}`), 0600))
	fileURI := testutil.FileURL(local)
	for _, target := range []string{server.URL, fileURI} {
		args := map[string]argmatcher.Matcher{"value": {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": target}}}
		specs := []models.ToolSpecParameters{{Tool: "read", Args: args}}
		for _, params := range []models.GraderParameters{
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: args}}},
			models.ToolConstraintGraderParameters{ExpectTools: specs},
			models.ToolConstraintGraderParameters{RejectTools: specs},
			models.ToolConstraintGraderParameters{AllowOnly: &specs},
		} {
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			if target == fileURI {
				_, err := Create("legacy", params)
				require.NoError(t, err, "runtime readable external schemas remain valid")
				require.ErrorAs(t, ValidateConfig("already-compiled", params), &loadErr)
				require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			}
			require.Zero(t, requests.Load(), "HTTP schema requests must remain exactly zero")
		}
	}
}
