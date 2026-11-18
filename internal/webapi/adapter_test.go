package webapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runneradapter"
	"github.com/stretchr/testify/require"
)

func TestAdapterTranslateRoute(t *testing.T) {
	mux := http.NewServeMux()
	RegisterAdapterRoutes(mux)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request = bytes.TrimSpace(request)
	descriptor := `{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"w-1","operation":"submit","request":` + string(request) + `}`

	server := httptest.NewServer(mux)
	defer server.Close()
	post := func(body, contentType string) *http.Response {
		response, err := server.Client().Post(server.URL+"/api/v1/adapter/translate", contentType, strings.NewReader(body))
		require.NoError(t, err)
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	read := func(response *http.Response) []byte {
		data, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return data
	}
	ok := post(descriptor, "application/json")
	body := read(ok)
	require.Equal(t, http.StatusOK, ok.StatusCode, string(body))
	translation, err := runneradapter.ParseTranslation(body)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/submissions", translation.Call.HTTP.Path)
	require.Contains(t, translation.Call.Input, string(request), "the request is forwarded byte-for-byte")

	rejected := post(strings.Replace(descriptor, `"work_id"`, `"queue":"gpu","work_id"`, 1), "application/json")
	require.Equal(t, http.StatusUnprocessableEntity, rejected.StatusCode)
	translation, err = runneradapter.ParseTranslation(read(rejected))
	require.NoError(t, err)
	require.Equal(t, runneradapter.CodeDescriptorUnsupported, translation.Diagnostics[0].Code)

	require.Equal(t, http.StatusUnsupportedMediaType, post(descriptor, "text/plain").StatusCode)
}
