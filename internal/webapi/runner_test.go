package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerNegotiationTransport(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	valid := `{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[],"optional_capabilities":[]}`
	for _, tc := range []struct {
		name, media, body string
		status            int
	}{
		{"valid", "application/json", valid, http.StatusOK},
		{"charset", "application/json; charset=utf-8", valid, http.StatusOK},
		{"invalid-json", "application/json", `{`, http.StatusOK},
		{"missing-media", "", valid, http.StatusUnsupportedMediaType},
		{"wrong-media", "text/plain", valid, http.StatusUnsupportedMediaType},
		{"oversized", "application/json", strings.Repeat(" ", runnercontract.MaxRequestBytes+1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := server.Client().Post(server.URL+"/api/v1/runner/negotiate", tc.media, strings.NewReader(tc.body))
			require.NoError(t, err)
			defer response.Body.Close() //nolint:errcheck
			require.Equal(t, tc.status, response.StatusCode)
			var body runnercontract.Response
			require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
			if tc.name == "valid" || tc.name == "charset" {
				require.Equal(t, "compatible", body.Status)
			} else {
				require.Equal(t, "invalid_request", body.Status)
				require.Empty(t, body.Negotiation.Selected)
			}
		})
	}
	response, err := server.Client().Get(server.URL + "/api/v1/runner/capabilities")
	require.NoError(t, err)
	defer response.Body.Close() //nolint:errcheck
	var advertisement runnercontract.Advertisement
	require.NoError(t, json.NewDecoder(response.Body).Decode(&advertisement))
	require.Equal(t, runnercontract.Capabilities(), advertisement)
}

func TestRunnerNegotiationPartialUploadIsBounded(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	require.NoError(t, conn.SetDeadline(time.Now().Add(8*time.Second)))
	_, err = fmt.Fprint(conn, "POST /api/v1/runner/negotiate HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{")
	require.NoError(t, err)
	response, err := io.ReadAll(conn)
	require.NoError(t, err)
	require.Contains(t, string(response), "400 Bad Request")
	require.Contains(t, string(response), `"status":"invalid_request"`)
	require.NotContains(t, string(response), `"status":"compatible"`)
}

func TestRunnerNegotiationNoDeadlineSupportFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/negotiate", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "setting negotiation read deadline")
}
