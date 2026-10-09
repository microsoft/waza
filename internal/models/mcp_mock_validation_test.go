package models

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
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

type matcherTestLoader func(string) (any, error)

func (load matcherTestLoader) Load(location string) (any, error) {
	return load(location)
}

func TestMCPMatcherPreservesExplicitResourceLoader(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		if err != nil {
			t.Errorf("writing schema sentinel: %v", err)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "valid-schema.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"object"}`), 0600))
	fileURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	sentinel := errors.New("test loader refuses external resources")
	for _, location := range []string{fileURI, server.URL + "/schema"} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			deny := matcherTestLoader(func(got string) (any, error) {
				calls++
				require.Equal(t, location, got)
				return nil, sentinel
			})
			response := MCPMockResponse{MatchSchema: map[string]any{"$ref": location}}
			err := ValidateMCPMockResponseWithLoader(response, deny)
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, err, &loadErr)
			require.ErrorIs(t, loadErr.Err, sentinel)
			require.Equal(t, 1, calls)
			require.Zero(t, requests.Load())
		})
	}
	response := MCPMockResponse{MatchSchema: map[string]any{"$ref": fileURI}}
	require.NoError(t, ValidateMCPMockResponse(response), "legacy defaults still resolve files")
	require.NoError(t, ValidateMCPMockResponseWithLoader(response, nil))
}

func TestMCPMatcherLocalReferencesAndSuppliedResources(t *testing.T) {
	calls := 0
	loader := matcherTestLoader(func(location string) (any, error) {
		calls++
		require.Equal(t, "https://schema.invalid/supplied", location)
		return map[string]any{"type": "object"}, nil
	})
	local := MCPMockResponse{MatchSchema: map[string]any{
		"$defs": map[string]any{"value": map[string]any{"type": "object"}},
		"$ref":  "#/$defs/value",
	}}
	require.NoError(t, ValidateMCPMockResponseWithLoader(local, loader))
	require.Zero(t, calls, "registered local resources must not use the external loader")
	external := MCPMockResponse{MatchSchema: map[string]any{"$ref": "https://schema.invalid/supplied"}}
	require.NoError(t, ValidateMCPMockResponseWithLoader(external, loader))
	require.Equal(t, 1, calls)
}
