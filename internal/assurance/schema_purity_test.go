package assurance

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
)

func TestObserveMechanicalExternalSchemasAreNotAssessed(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(`{"type":"object"}`)); err != nil {
			t.Errorf("writing sentinel schema: %v", err)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "available-schema.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"object"}`), 0o600))
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	fileURI := (&url.URL{Scheme: "file", Path: uriPath}).String()
	for _, target := range []struct{ name, uri string }{
		{"available-http", server.URL + "/schema"},
		{"available-file", fileURI},
		{"absent-file", fileURI},
	} {
		t.Run(target.name, func(t *testing.T) {
			if target.name == "absent-file" {
				require.NoError(t, os.Remove(path))
			}
			for _, output := range []string{"{}", "not-json"} {
				executor := &executionGuard{}
				result := ObserveMechanical(t.Context(), referenceInput(
					models.JSONSchemaGraderParameters{Schema: map[string]any{"$ref": target.uri}},
					&graders.Context{Output: output, Executor: executor},
				))
				require.Equal(t, NotAssessed, result.State)
				require.ErrorIs(t, result.Err, schemaloader.ErrExternalReference)
				require.Nil(t, result.Result)
				require.Zero(t, executor.calls)
				require.Zero(t, requests.Load())
			}
		})
	}
}
