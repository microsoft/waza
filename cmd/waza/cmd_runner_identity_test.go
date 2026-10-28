package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

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
		for _, action := range []string{"validate", "run"} {
			t.Run(tc.Name+"/"+action, func(t *testing.T) {
				body, err := json.Marshal(tc.Request)
				require.NoError(t, err)
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
				var terminal runnerterminal.Envelope
				code := decodeRunnerResponse(t, action, output.Bytes(), &cli, &terminal)
				if code == 0 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				response, err := server.Client().Post(server.URL+"/api/v1/runner/"+action, "application/json", bytes.NewReader(body))
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, response.StatusCode)
				var api runnerunit.Report
				var raw json.RawMessage
				require.NoError(t, json.NewDecoder(response.Body).Decode(&raw))
				decodeRunnerResponse(t, action, raw, &api, &terminal)
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
