package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerIdentityOriginalWireCLIHTTP(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	base, err := runnercontract.Parse(data)
	require.NoError(t, err)
	var factories atomic.Int32
	service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		require.Equal(t, "mock", cfg.EngineType)
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutesWithService(mux, service)
	server := httptest.NewServer(mux)
	defer server.Close()

	encode := func(request runnercontract.Request) []byte {
		t.Helper()
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		require.NoError(t, encoder.Encode(request))
		return encoded.Bytes()
	}
	html := *base
	html.Input.Task = strings.Replace(html.Input.Task, "prompt: hello", "prompt: hello "+strings.Repeat("<>&", 250_000), 1)
	htmlWire := encode(html)
	htmlIdentity, err := runneridentity.DeriveJSON(htmlWire)
	require.NoError(t, err)
	for _, control := range []string{"ordinary", "html", "less-than", "partially-escaped", "near-limit", "at-limit", "over-limit", "escaped-over-limit"} {
		t.Run(control, func(t *testing.T) {
			request := *base
			prompt := strings.Repeat("<>&", 250_000)
			switch control {
			case "ordinary":
				prompt = strings.Repeat("abc", 250_000)
			case "less-than":
				prompt = strings.Repeat("<", 750_000)
			}
			request.Input.Task = strings.Replace(request.Input.Task, "prompt: hello", "prompt: hello "+prompt, 1)
			require.NotEqual(t, base.Input.Task, request.Input.Task)
			body := encode(request)
			require.Len(t, body, len(htmlWire), "HTML and ordinary controls must have identical original wire sizes")
			escaped, err := json.Marshal(request)
			require.NoError(t, err)
			if control != "ordinary" {
				require.Greater(t, len(escaped), runnercontract.MaxRequestBytes)
				require.Contains(t, string(body), prompt)
			} else {
				require.Less(t, len(escaped), runnercontract.MaxRequestBytes)
			}
			limit := 0
			switch control {
			case "near-limit":
				limit = runnercontract.MaxRequestBytes - 1
			case "at-limit":
				limit = runnercontract.MaxRequestBytes
			case "over-limit":
				limit = runnercontract.MaxRequestBytes + 1
			case "partially-escaped":
				body = bytes.ReplaceAll(body, []byte("<"), []byte(`\u003c`))
				require.NotContains(t, string(body), "<")
				require.Less(t, len(body), runnercontract.MaxRequestBytes)
			case "escaped-over-limit":
				body = escaped
			}
			if limit != 0 {
				require.Less(t, len(body), limit)
				body = append(body, bytes.Repeat([]byte(" "), limit-len(body))...)
				require.Len(t, body, limit)
			}
			original := bytes.Clone(body)
			rejected := control == "over-limit" || control == "escaped-over-limit"
			expected, deriveErr := runneridentity.DeriveJSON(body)
			if rejected {
				require.ErrorContains(t, deriveErr, "exceeds")
				require.Nil(t, expected)
			} else {
				require.NoError(t, deriveErr)
				if control != "ordinary" && control != "less-than" {
					require.Equal(t, htmlIdentity, expected, "wire whitespace/escaping must not change identity")
				} else {
					require.NotEqual(t, htmlIdentity, expected, "material prompt changes must change identity")
				}
			}
			for _, action := range []string{"validate", "run"} {
				for _, transport := range []string{"cli", "http"} {
					t.Run(action+"/"+transport, func(t *testing.T) {
						before := factories.Load()
						var report runnerunit.Report
						if transport == "cli" {
							command := newRunnerCommandWithService(service)
							command.SetArgs([]string{action})
							command.SetIn(bytes.NewReader(body))
							command.SetErr(&bytes.Buffer{})
							var output bytes.Buffer
							command.SetOut(&output)
							executeErr := command.Execute()
							decoder := json.NewDecoder(&output)
							require.NoError(t, decoder.Decode(&report))
							require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
							if rejected {
								exit, ok := errors.AsType[*ExitCodeError](executeErr)
								require.True(t, ok)
								require.Equal(t, 6, exit.Code)
							} else {
								require.NoError(t, executeErr, "%+v", report.Diagnostics)
							}
						} else {
							response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
							require.NoError(t, err)
							defer func() { require.NoError(t, response.Body.Close()) }()
							if rejected {
								require.Equal(t, http.StatusBadRequest, response.StatusCode)
								var failure struct {
									Error string `json:"error"`
								}
								require.NoError(t, json.NewDecoder(response.Body).Decode(&failure))
								require.Contains(t, failure.Error, "request body too large")
								require.Equal(t, before, factories.Load())
								return
							}
							require.Equal(t, http.StatusOK, response.StatusCode)
							require.NoError(t, json.NewDecoder(response.Body).Decode(&report))
						}
						require.Equal(t, original, body, "public calls must not mutate original wire")
						require.False(t, report.AdjacentWork)
						if rejected {
							require.Equal(t, "rejected", report.Status)
							require.Equal(t, 6, report.ExitCode())
							require.Nil(t, report.Identity)
							require.Nil(t, report.Resolved)
							require.Nil(t, report.Observation)
							require.Zero(t, report.Calls)
							require.Equal(t, before, factories.Load())
							return
						}
						require.Empty(t, report.Diagnostics)
						require.Equal(t, expected, report.Identity)
						require.Equal(t, request.Selection, *report.Resolved)
						require.Equal(t, "valid", report.Validation)
						require.Zero(t, report.ExitCode())
						if action == "run" {
							require.Equal(t, "completed", report.Status)
							require.Equal(t, int32(1), factories.Load()-before)
							require.Equal(t, 1, report.Calls)
							require.NotNil(t, report.Observation)
							require.Equal(t, models.StatusPassed, report.Observation.Status)
							require.Contains(t, report.Observation.FinalOutput, "hello "+prompt)
							require.Nil(t, report.Observation.Usage)
						} else {
							require.Equal(t, "ready", report.Status)
							require.Equal(t, before, factories.Load())
							require.Zero(t, report.Calls)
							require.Nil(t, report.Observation)
						}
					})
				}
			}
		})
	}
}

func TestRunnerIdentityFrozenCLIHTTP(t *testing.T) {
	data, err := os.ReadFile("../../internal/runnerunit/testdata/identities.json")
	require.NoError(t, err)
	var cases []struct {
		Name     string                  `json:"name"`
		Request  runnercontract.Request  `json:"request"`
		Identity runneridentity.Identity `json:"identity"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 11)
	factories := 0
	service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutesWithService(mux, service)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, tc := range cases {
		for _, suffix := range []string{"", ".0", "0e-1"} {
			for _, action := range []string{"validate", "run"} {
				t.Run(tc.Name+"/"+suffix+"/"+action, func(t *testing.T) {
					body, err := json.Marshal(tc.Request)
					require.NoError(t, err)
					if suffix != "" {
						var fields, selection map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(body, &fields))
						require.NoError(t, json.Unmarshal(fields["selection"], &selection))
						fields["timeout_ms"] = append(fields["timeout_ms"], suffix...)
						for _, field := range []string{"trial", "attempt"} {
							selection[field] = append(selection[field], suffix...)
						}
						fields["selection"], err = json.Marshal(selection)
						require.NoError(t, err)
						body, err = json.Marshal(fields)
						require.NoError(t, err)
						require.Contains(t, string(body), `"timeout_ms":`+string(fields["timeout_ms"]))
						require.Contains(t, string(body), `"trial":`+string(selection["trial"]))
						require.Contains(t, string(body), `"attempt":`+string(selection["attempt"]))
					}
					before := factories
					command := newRunnerCommandWithService(service)
					command.SetArgs([]string{action})
					command.SetIn(bytes.NewReader(body))
					command.SetErr(&bytes.Buffer{})
					var output bytes.Buffer
					command.SetOut(&output)
					// A grader failure is still a completed attempt and carries identity.
					err = command.Execute()
					var cli runnerunit.Report
					require.NoError(t, json.Unmarshal(output.Bytes(), &cli))
					if cli.ExitCode() == 0 {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, response.StatusCode)
					var api runnerunit.Report
					require.NoError(t, json.NewDecoder(response.Body).Decode(&api))
					require.NoError(t, response.Body.Close())
					for _, report := range []runnerunit.Report{cli, api} {
						require.Equal(t, &tc.Identity, report.Identity)
						require.Equal(t, tc.Request.Selection, *report.Resolved)
						require.False(t, report.AdjacentWork)
						if action == "run" {
							require.Equal(t, "completed", report.Status)
							require.Equal(t, 1, report.Calls)
							require.NotNil(t, report.Observation)
						} else {
							require.Equal(t, "ready", report.Status)
							require.Zero(t, report.Calls)
							require.Nil(t, report.Observation)
						}
					}
					expected := 0
					if action == "run" {
						expected = 2
					}
					require.Equal(t, expected, factories-before)
				})
			}
		}
	}
}
