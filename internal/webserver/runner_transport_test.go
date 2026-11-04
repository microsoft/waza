package webserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestRunnerProductionTransportAdmission(t *testing.T) {
	unit, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	directory := t.TempDir()
	t.Chdir(directory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".waza.yaml"), []byte("invalid: [must not read ambient config"), 0o600))
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(directory, "runtime-must-not-be-launched"))
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var externalCalls atomic.Int32
	http.DefaultTransport = runnerRejectingTransport{calls: &externalCalls}

	server, err := New(Config{ResultsDir: directory, NoBrowser: true})
	require.NoError(t, err)
	require.Zero(t, server.srv.ReadTimeout, "exercise production without a server-wide body timeout")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.srv.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.srv.Close())
		require.ErrorIs(t, <-done, http.ErrServerClosed)
	})

	for _, action := range []string{"negotiate", "validate", "run"} {
		t.Run(action, func(t *testing.T) {
			valid := string(unit)
			if action == "negotiate" {
				valid = `{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[],"optional_capabilities":[]}`
			}
			for _, tc := range []struct {
				name, media, body string
				partial           bool
				status            int
			}{
				{"missing-partial", "", "{", true, http.StatusUnsupportedMediaType},
				{"wrong-partial", "text/plain", "{", true, http.StatusUnsupportedMediaType},
				{"malformed-partial", "application/json; charset", "{", true, http.StatusUnsupportedMediaType},
				{"json-partial", "application/json", "{", true, http.StatusBadRequest},
				{"missing-complete", "", valid, false, http.StatusUnsupportedMediaType},
				{"wrong-complete", "text/plain", valid, false, http.StatusUnsupportedMediaType},
				{"json-complete", "application/json", valid, false, http.StatusOK},
				{"charset-complete", "application/json; charset=utf-8", valid, false, http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					status := tc.status
					if action == "run" && tc.name == "json-partial" {
						status = http.StatusRequestTimeout
					}
					conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
					require.NoError(t, err)
					defer conn.Close() //nolint:errcheck
					require.NoError(t, conn.SetDeadline(time.Now().Add(7*time.Second)))
					length := len(tc.body)
					if tc.partial {
						length = 100
					}
					media := ""
					if tc.media != "" {
						media = "Content-Type: " + tc.media + "\r\n"
					}
					// Deliberately keep the connection and request body open: Connection: close
					// would bypass net/http's unread-body drain on an early rejection.
					_, err = fmt.Fprintf(conn, "POST /api/v1/runner/%s HTTP/1.1\r\nHost: localhost\r\n%sContent-Length: %d\r\n\r\n%s", action, media, length, tc.body)
					require.NoError(t, err)
					reader := bufio.NewReader(conn)
					response, err := http.ReadResponse(reader, nil)
					require.NoError(t, err, "partial upload must finish without the client sending EOF")
					require.Equal(t, status, response.StatusCode)
					if action == "run" {
						raw, err := io.ReadAll(response.Body)
						require.NoError(t, err)
						var binding *runnerterminal.Binding
						if status == http.StatusOK {
							admitted := runnerunit.Validate(unit)
							require.Equal(t, "ready", admitted.Status)
							binding = &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
								GraderIDs: []string{"_output_contains"}}
						}
						envelope, err := runnerterminal.Validate(raw, binding)
						require.NoError(t, err, "%s", raw)
						require.Equal(t, fmt.Sprint(envelope.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
						require.Equal(t, 1, bytes.Count(raw, []byte{'\n'}))
						if status == http.StatusOK {
							require.Equal(t, runnerterminal.Success, envelope.Outcome)
							require.Equal(t, "valid", envelope.Validation)
							require.Empty(t, envelope.Diagnostics)
							require.Equal(t, binding.Identity, envelope.Identity)
							require.Equal(t, binding.Selection, *envelope.Selection)
							require.Equal(t, 1, envelope.EngineCalls)
							require.NotNil(t, envelope.Observation)
							require.Equal(t, binding.Selection, envelope.Observation.Selection)
							require.Equal(t, binding.Selection.Trial, envelope.Observation.RunNumber)
							require.Equal(t, binding.Selection.Attempt, envelope.Observation.Attempts)
							require.Equal(t, "passed", envelope.Observation.Status)
							require.Equal(t, []runnerterminal.Verdict{{Identifier: "_output_contains", Passed: true}},
								envelope.Observation.Validations)
						} else {
							require.Zero(t, envelope.EngineCalls)
							require.Nil(t, envelope.Identity)
							require.Nil(t, envelope.Selection)
							require.Nil(t, envelope.Observation)
							require.Len(t, envelope.Diagnostics, 1)
							if status == http.StatusUnsupportedMediaType {
								require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
								require.Equal(t, 6, envelope.ExitCode)
								require.Contains(t, envelope.Diagnostics[0].Message, "Content-Type")
							} else {
								require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
								require.Equal(t, 4, envelope.ExitCode)
								require.Contains(t, envelope.Diagnostics[0].Message, "timeout")
							}
						}
					} else if action == "negotiate" {
						var body runnercontract.Response
						require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
						if tc.status == http.StatusOK {
							require.Equal(t, "compatible", body.Status)
						} else {
							require.Equal(t, "invalid_request", body.Status)
							require.Empty(t, body.Negotiation.Selected)
							require.Len(t, body.Diagnostics, 1)
							if tc.status == http.StatusUnsupportedMediaType {
								require.Contains(t, body.Diagnostics[0].Message, "Content-Type")
							} else {
								require.Contains(t, body.Diagnostics[0].Message, "timeout")
							}
						}
					} else if tc.status == http.StatusOK {
						var body runnerunit.Report
						require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
						require.Equal(t, "valid", body.Validation)
						require.False(t, body.AdjacentWork)
						require.NotNil(t, body.Resolved)
						require.Empty(t, body.Diagnostics)
						require.Equal(t, "ready", body.Status)
						require.Zero(t, body.Calls)
						require.Nil(t, body.Observation)
					} else {
						var body struct {
							Error string `json:"error"`
						}
						require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
						if tc.status == http.StatusUnsupportedMediaType {
							require.Equal(t, "runner requires Content-Type: application/json", body.Error)
						} else {
							require.Contains(t, body.Error, "reading runner request:")
							require.Contains(t, body.Error, "timeout")
						}
					}
					require.NoError(t, response.Body.Close())
					if tc.partial || action == "run" && status == http.StatusUnsupportedMediaType {
						require.True(t, response.Close, "unadmitted run bodies and incomplete uploads require a fresh connection")
						return
					}
					require.False(t, response.Close, "ordinary admission must preserve keep-alive")
					_, err = fmt.Fprint(conn, "GET /api/v1/runner/capabilities HTTP/1.1\r\nHost: localhost\r\n\r\n")
					require.NoError(t, err)
					response, err = http.ReadResponse(reader, nil)
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, response.StatusCode)
					var advertisement runnercontract.Advertisement
					require.NoError(t, json.NewDecoder(response.Body).Decode(&advertisement))
					require.NoError(t, response.Body.Close())
					require.Equal(t, runnercontract.Capabilities(), advertisement)
				})
			}
		})
	}
	require.FileExists(t, filepath.Join(directory, ".waza.yaml"))
	require.NoFileExists(t, filepath.Join(directory, "runtime-must-not-be-launched"))
	require.Zero(t, externalCalls.Load(), "admission must not make external HTTP calls")
}

type runnerRejectingTransport struct{ calls *atomic.Int32 }

func (transport runnerRejectingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, fmt.Errorf("external HTTP is forbidden during negotiation admission")
}
