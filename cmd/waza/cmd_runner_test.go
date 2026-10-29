package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerCLIHTTPParity(t *testing.T) {
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	supported, err := os.ReadFile("../../examples/runner/negotiation.json")
	require.NoError(t, err)
	full, err := os.ReadFile("../../internal/runnercontract/testdata/request.json")
	require.NoError(t, err)
	for _, data := range [][]byte{
		supported, full,
		[]byte(strings.Replace(string(supported), `"2.0", "1.0"`, `"3.0"`, 1)),
		[]byte(strings.Replace(string(supported), `"negotiation"`, `"NEGOTIATION"`, 1)),
		[]byte(strings.Replace(string(supported), `"negotiation"`, `"inline.native-text"`, 1)),
		[]byte(strings.Replace(string(supported), `"negotiation"`, `"identity.sha256-json-v1"`, 1)),
		[]byte(`{"schema_version":"1.0","versions":["1.0"],"required_capabilities":null,"optional_capabilities":[]}`),
		[]byte(`{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[],"optional_capabilities":[],"versions":["2.0"]}`),
	} {
		cmd := newRootCommand()
		cmd.SetArgs([]string{"runner", "negotiate"})
		cmd.SetIn(bytes.NewReader(data))
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		err := cmd.Execute()
		var response runnercontract.Response
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &response), stdout.String())
		if response.ExitCode() == 0 {
			require.NoError(t, err)
		} else {
			exit, ok := errors.AsType[*ExitCodeError](err)
			require.True(t, ok)
			require.Equal(t, response.ExitCode(), exit.Code)
		}
		require.Empty(t, stderr.String())
		httpResponse, err := server.Client().Post(server.URL+"/api/v1/runner/negotiate", "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		var api runnercontract.Response
		require.NoError(t, json.NewDecoder(httpResponse.Body).Decode(&api))
		require.NoError(t, httpResponse.Body.Close())
		require.Equal(t, http.StatusOK, httpResponse.StatusCode)
		require.Equal(t, response, api)
	}
}

func TestRunnerCompleteRequestCLIHTTPParity(t *testing.T) {
	full, err := os.ReadFile("../../internal/runnercontract/testdata/request.json")
	require.NoError(t, err)
	base := strings.Replace(string(full), `["exact-unit", "inline.mock-text", "terminal-result"]`, `["negotiation"]`, 1)
	require.NotEqual(t, string(full), base)
	expected := runnercontract.Response{
		Kind: "waza.runner-negotiation", SchemaVersion: "1.0", Status: "compatible",
		Negotiation: runnercontract.Negotiation{
			Version: "1.0", RequestedVersions: []string{"1.0"},
			RequiredCapabilities: []string{"negotiation"}, OptionalCapabilities: []string{},
			Available: []string{"negotiation", "exact-unit", "inline.mock-text"}, Selected: []string{"negotiation"}, UnavailableOptional: []string{},
		},
		Diagnostics: []runnercontract.Diagnostic{},
	}
	directory := t.TempDir()
	t.Chdir(directory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".waza.yaml"), []byte("invalid: [must not read ambient config"), 0o600))
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(directory, "runtime-must-not-be-launched"))
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var networkCalls atomic.Int32
	http.DefaultTransport = runnerRejectingTransport{calls: &networkCalls}
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Transport = transport

	for _, lineEnding := range []string{"\n", "\r\n"} {
		name := "LF"
		if lineEnding == "\r\n" {
			name = "CRLF"
		}
		t.Run(name, func(t *testing.T) {
			base := strings.ReplaceAll(strings.ReplaceAll(base, "\r\n", "\n"), "\n", lineEnding)
			require.Positive(t, strings.Count(base, lineEnding))
			require.Equal(t, strings.Count(base, "\n"), strings.Count(base, lineEnding), "all fixture lines must use the selected wire ending")
			require.NotContains(t, strings.ReplaceAll(base, lineEnding, ""), "\r", "no stray carriage returns")
			for _, tc := range []struct {
				name, old, replacement, field string
			}{
				{name: "canonical"},
				{name: "exponent timeout", old: `"timeout_ms": 60000`, replacement: `"timeout_ms": 6e4`},
				{name: "decimal trial", old: `"trial": 2`, replacement: `"trial": 2.0`},
				{name: "exponent attempt", old: `"attempt": 2`, replacement: `"attempt": 20e-1`},
				{name: "paid permission", old: `"allow_paid_execution": false`, replacement: `"allow_paid_execution": true`},
				{name: "uninterpreted YAML", old: `schemaVersion:`, replacement: `invalid: [`, field: ""},
				{name: "zero timeout", old: `"timeout_ms": 60000`, replacement: `"timeout_ms": 0`, field: "timeout_ms"},
				{name: "missing permission", old: `,` + "\n" + `  "allow_paid_execution": false`, replacement: "", field: "allow_paid_execution"},
				{name: "null permission", old: `"allow_paid_execution": false`, replacement: `"allow_paid_execution": null`, field: "allow_paid_execution"},
				{name: "fractional trial", old: `"trial": 2`, replacement: `"trial": 2.1`, field: "trial"},
				{name: "too large trial", old: `"trial": 2`, replacement: `"trial": 2147483648`, field: "trial"},
				{name: "fractional timeout", old: `"timeout_ms": 60000`, replacement: `"timeout_ms": 60000.000000000001`, field: "timeout_ms"},
				{name: "unknown nested field", old: `"trial": 2`, replacement: `"trial": 2, "extra": 1`, field: "extra"},
				{name: "duplicate nested key", old: `"trial": 2`, replacement: `"trial": 2, "trial": 3`, field: "duplicate JSON object key"},
				{name: "escaped duplicate key", old: `"trial": 2`, replacement: `"trial": 2, "tri\u0061l": 3`, field: "duplicate JSON object key"},
				{name: "invalid Unicode", old: `"run_key": "conformance"`, replacement: `"run_key": "\ud800"`, field: "Unicode"},
				{name: "wrong case field", old: `"timeout_ms": 60000`, replacement: `"TIMEOUT_MS": 60000`, field: "timeout_ms"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					data := base
					if tc.old != "" {
						old := strings.ReplaceAll(tc.old, "\n", lineEnding)
						replacement := strings.ReplaceAll(tc.replacement, "\n", lineEnding)
						require.Equal(t, 1, strings.Count(data, old), "mutation must exercise the intended wire value")
						data = strings.Replace(data, old, replacement, 1)
						require.NotEqual(t, base, data)
					}
					if tc.name == "missing permission" {
						require.NotContains(t, data, `"allow_paid_execution"`, "the permission must actually be absent on the wire")
					}
					cmd := newRootCommand()
					cmd.SetArgs([]string{"runner", "negotiate"})
					cmd.SetIn(strings.NewReader(data))
					var stdout, stderr bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(&stderr)
					commandErr := cmd.Execute()
					var cli runnercontract.Response
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &cli), stdout.String())
					require.Empty(t, stderr.String())
					if tc.field == "" {
						require.NoError(t, commandErr)
						require.Equal(t, expected, cli)
					} else {
						exit, ok := errors.AsType[*ExitCodeError](commandErr)
						require.True(t, ok)
						require.Equal(t, 6, exit.Code)
						require.Equal(t, "invalid_request", cli.Status)
						require.Empty(t, cli.Negotiation.Selected)
						require.Len(t, cli.Diagnostics, 1)
						require.Equal(t, "invalid_request", cli.Diagnostics[0].Code)
						require.Contains(t, cli.Diagnostics[0].Message, tc.field)
						if tc.field == "timeout_ms" && tc.name != "wrong case field" || tc.field == "allow_paid_execution" {
							require.NotContains(t, cli.Diagnostics[0].Message, "additional properties")
						}
					}
					response, err := client.Post(server.URL+"/api/v1/runner/negotiate", "application/json", strings.NewReader(data))
					require.NoError(t, err)
					var api runnercontract.Response
					require.NoError(t, json.NewDecoder(response.Body).Decode(&api))
					require.NoError(t, response.Body.Close())
					require.Equal(t, http.StatusOK, response.StatusCode)
					require.Equal(t, cli, api)
				})
			}
		})
	}
	require.Zero(t, networkCalls.Load(), "negotiation must not access the network")
}

func TestRunnerDoesNotCheckUpdatesOrAdvertiseDeferredCapabilities(t *testing.T) {
	root := newRootCommand()
	command, _, err := root.Find([]string{"runner", "negotiate"})
	require.NoError(t, err)
	require.False(t, shouldRunUpdateCheck(command, false))
	root.SetArgs([]string{"runner", "capabilities"})
	var output bytes.Buffer
	root.SetOut(&output)
	require.NoError(t, root.Execute())
	var advertisement runnercontract.Advertisement
	require.NoError(t, json.Unmarshal(output.Bytes(), &advertisement))
	require.Equal(t, []string{"negotiation", "exact-unit", "inline.mock-text"}, advertisement.Capabilities)
	require.NotContains(t, output.String(), "inline.native-text")
	require.NotContains(t, output.String(), "terminal-result")
}

func TestRunnerArgumentAndReadWriteFailures(t *testing.T) {
	for _, args := range [][]string{{"unexpected"}, {"--unknown"}} {
		cmd := newRunnerCommand()
		cmd.SetArgs(append([]string{"negotiate"}, args...))
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&bytes.Buffer{})
		require.Error(t, cmd.Execute())
		var response runnercontract.Response
		require.NoError(t, json.Unmarshal(output.Bytes(), &response))
		require.Equal(t, "invalid_request", response.Status)
	}
	cmd := newRunnerCommand()
	cmd.SetOut(errorWriter{})
	err := emitNegotiation(cmd, runnercontract.Rejected("test"))
	exit, ok := errors.AsType[*ExitCodeError](err)
	require.True(t, ok)
	require.Equal(t, 2, exit.Code)
	cmd = newRunnerCommand()
	cmd.SetArgs([]string{"negotiate"})
	cmd.SetIn(errorReader{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute())
	require.Contains(t, output.String(), "injected read failure")
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("injected write failure") }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func TestRunnerNegotiationCanceledOpenInput(t *testing.T) {
	input, producer := io.Pipe()
	defer input.Close()    //nolint:errcheck
	defer producer.Close() //nolint:errcheck
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := newRunnerCommand()
	cmd.SetArgs([]string{"negotiate"})
	cmd.SetIn(input)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	_, err := producer.Write([]byte("{"))
	require.NoError(t, err, "read must be active before cancellation")
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, output.String(), "negotiation input interrupted")
		require.Contains(t, output.String(), `"status":"invalid_request"`)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation waited for producer EOF")
	}
}

func TestRunnerNegotiationIgnoresAmbientRuntimeAndNetwork(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".waza.yaml"), []byte("invalid: [ambient configuration must not be read"), 0o600))
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(directory, "runtime-must-not-be-launched"))
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var networkCalls atomic.Int32
	http.DefaultTransport = runnerRejectingTransport{calls: &networkCalls}
	for _, capability := range []string{"negotiation", "inline.native-text", "exact-unit", "terminal-result"} {
		cmd := newRootCommand()
		cmd.SetArgs([]string{"runner", "negotiate"})
		cmd.SetIn(strings.NewReader(fmt.Sprintf(`{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[%q],"optional_capabilities":[]}`, capability)))
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		err := cmd.Execute()
		var response runnercontract.Response
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
		if capability == "negotiation" || capability == "exact-unit" {
			require.NoError(t, err)
			require.Equal(t, "compatible", response.Status)
		} else {
			require.Error(t, err)
			require.Equal(t, "unsupported_capability", response.Status)
		}
		require.NotContains(t, stdout.String(), "ambient configuration")
	}
	require.Zero(t, networkCalls.Load(), "neither schema lookup nor update checks may access the network")
}

type runnerRejectingTransport struct{ calls *atomic.Int32 }

func (transport runnerRejectingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("runner must not make network requests")
}
