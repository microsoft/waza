package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerCopilotHTTPTrustBoundary(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		protocol := "http1"
		if http2 {
			protocol = "http2"
		}
		for _, control := range []struct {
			name, host, origin, site, remote string
			denied, mock, malformed          bool
		}{
			{name: "local nonbrowser"},
			{name: "same origin", origin: "same", site: "same-origin"},
			{name: "localhost", host: "localhost:3000", origin: "same"},
			{name: "IPv6 authority", host: "[::1]:3000", origin: "same"},
			{name: "rebound host", host: "attacker.example", origin: "same", denied: true},
			{name: "foreign origin", origin: "https://attacker.example", denied: true},
			{name: "opaque origin", origin: "null", denied: true},
			{name: "wrong scheme", origin: "wrong-scheme", denied: true},
			{name: "wrong port", origin: "wrong-port", denied: true},
			{name: "cross site", site: "cross-site", denied: true},
			{name: "same site is not same origin", site: "same-site", denied: true},
			{name: "remote peer", remote: "192.0.2.1:5000", origin: "same", denied: true},
			{name: "remote mock preserved", remote: "192.0.2.1:5000", host: "remote.example", origin: "https://remote.example", mock: true},
			{name: "malformed native cannot bypass admission", host: "attacker.example", malformed: true},
		} {
			t.Run(protocol+"/"+control.name, func(t *testing.T) {
				service, clients := runnerSDKService(t, "")
				if control.mock {
					service = runnerunit.NewService(nil)
				}
				mux := http.NewServeMux()
				webapi.RegisterRunnerRoutesWithService(mux, service)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if control.remote != "" {
						// Only the peer-address branch is synthesized; request
						// parsing, authority/origin and execution use net/http.
						r.RemoteAddr = control.remote
					}
					mux.ServeHTTP(w, r)
				}))
				server.EnableHTTP2 = http2
				if http2 {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				request := copilotRunnerRequest(t)
				if control.mock {
					request.RequiredCapabilities = []string{"exact-unit", "inline.mock-text"}
					request.Input.Eval = strings.ReplaceAll(request.Input.Eval, "copilot-sdk", "mock")
				}
				data, err := json.Marshal(request)
				require.NoError(t, err)
				if control.malformed {
					data = append(data, '}')
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/runner/run", bytes.NewReader(data))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				if control.host != "" {
					req.Host = control.host
				}
				origin := control.origin
				scheme := "http"
				if http2 {
					scheme = "https"
				}
				switch origin {
				case "same":
					origin = scheme + "://" + req.Host
				case "wrong-scheme":
					origin = map[bool]string{false: "https", true: "http"}[http2] + "://" + req.Host
				case "wrong-port":
					origin = scheme + "://localhost:1"
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				req.Header.Set("Sec-Fetch-Site", control.site)
				// Untrusted forwarded headers never authorize a hostile request.
				req.Header.Set("X-Forwarded-For", "127.0.0.1")
				req.Header.Set("X-Forwarded-Host", "localhost:3000")
				response, err := server.Client().Do(req)
				require.NoError(t, err)
				defer func() { require.NoError(t, response.Body.Close()) }()
				require.Equal(t, map[bool]int{false: 1, true: 2}[http2], response.ProtoMajor)
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				envelope := validateRunnerEnvelope(t, raw, request)
				if control.denied {
					require.Equal(t, http.StatusForbidden, response.StatusCode)
					require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
					require.Equal(t, "native_http_denied", envelope.Diagnostics[0].Code)
					require.Nil(t, envelope.Identity)
					require.Nil(t, envelope.Observation)
					require.Zero(t, envelope.EngineCalls)
					require.Empty(t, *clients, "host/origin rejection must precede runtime construction")
				} else if control.malformed {
					require.Equal(t, http.StatusOK, response.StatusCode)
					require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
					require.Empty(t, *clients)
					require.Zero(t, envelope.EngineCalls)
				} else {
					require.Equal(t, http.StatusOK, response.StatusCode)
					require.Equal(t, 1, envelope.EngineCalls)
					require.NotNil(t, envelope.Observation)
					if control.mock {
						require.Equal(t, "mock", envelope.Observation.ExecutionMode)
						require.Empty(t, *clients)
					} else {
						require.Equal(t, runnerterminal.Success, envelope.Outcome)
						require.Len(t, *clients, 1)
						require.EqualValues(t, 1, (*clients)[0].sends.Load())
					}
				}
			})
		}
	}
}
