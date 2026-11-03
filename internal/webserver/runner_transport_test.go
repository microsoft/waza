package webserver

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

const runnerTransportActionEnv = "WAZA_RUNNER_TRANSPORT_ACTION"

func TestRunnerProductionTransportAdmission(t *testing.T) {
	if action := os.Getenv(runnerTransportActionEnv); action != "" {
		runRunnerProductionTransportAdmission(t, action)
		return
	}

	actions := []string{"negotiate", "validate", "run"}
	coverDir := flag.Lookup("test.gocoverdir").Value.String()
	results := make([]struct {
		output []byte
		err    error
		pid    int
	}, len(actions))
	var group sync.WaitGroup
	for index, action := range actions {
		group.Add(1)
		go func() {
			defer group.Done()
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunnerProductionTransportAdmission$")
			// GOCOVERDIR alone does not make test binaries emit into the parent's coverage directory.
			if coverDir != "" {
				command.Args = append(command.Args, "-test.gocoverdir="+coverDir)
			}
			command.Env = append(os.Environ(), runnerTransportActionEnv+"="+action)
			results[index].output, results[index].err = command.CombinedOutput()
			if command.Process != nil {
				results[index].pid = command.Process.Pid
			}
		}()
	}
	group.Wait()
	for index, action := range actions {
		t.Run(action, func(t *testing.T) {
			require.NoError(t, results[index].err, "%s", results[index].output)
			if coverDir != "" {
				counters, err := filepath.Glob(filepath.Join(coverDir, fmt.Sprintf("covcounters.*.%d.*", results[index].pid)))
				require.NoError(t, err)
				require.Len(t, counters, 1, "each action must contribute counters to the parent's coverage directory")
			}
		})
	}
}

func runRunnerProductionTransportAdmission(t *testing.T, action string) {
	require.Contains(t, []string{"negotiate", "validate", "run"}, action)
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
			require.Equal(t, tc.status, response.StatusCode)
			if action == "negotiate" {
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
				if action == "validate" {
					require.Equal(t, "ready", body.Status)
					require.Zero(t, body.Calls)
					require.Nil(t, body.Observation)
				} else {
					require.Equal(t, "completed", body.Status)
					require.Equal(t, 1, body.Calls)
					require.NotNil(t, body.Observation)
					require.Contains(t, body.Observation.FinalOutput, "hello")
				}
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
			if tc.partial {
				require.True(t, response.Close, "incomplete body must not be reused as a new request")
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
	require.FileExists(t, filepath.Join(directory, ".waza.yaml"))
	require.NoFileExists(t, filepath.Join(directory, "runtime-must-not-be-launched"))
	require.Zero(t, externalCalls.Load(), "admission must not make external HTTP calls")
}

type runnerRejectingTransport struct{ calls *atomic.Int32 }

func (transport runnerRejectingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, fmt.Errorf("external HTTP is forbidden during negotiation admission")
}
