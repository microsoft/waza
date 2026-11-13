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
	"testing"

	"github.com/microsoft/waza/internal/executionbudget"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerBudgetsCLIAndHTTP(t *testing.T) {
	command := newRunnerCommandWithService(runnerunit.NewService(nil))
	command.SetArgs([]string{"budgets"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	require.NoError(t, command.ExecuteContext(t.Context()))
	var capabilities executionbudget.Capabilities
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &capabilities))
	require.Equal(t, executionbudget.HostCapabilities(), capabilities)

	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutesWithService(mux, runnerunit.NewService(nil))
	server := httptest.NewServer(mux)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/v1/runner/budgets")
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.JSONEq(t, strings.TrimSpace(stdout.String()), string(body))
}

func TestRunnerRunEnforcesAndReportsBudgets(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, budgets string
		want          runnerterminal.Outcome
		state, code   string
	}{
		// Exponent notation exercises exact-integer normalization of limits.
		{"within budget", `{"limits":{"wall_time_ms":6e4,"model_turns":1}}`, runnerterminal.Success, executionbudget.StateWithinBudget, ""},
		{"model turns exceeded", `{"limits":{"model_turns":0}}`, runnerterminal.Canceled, executionbudget.StateTerminated, "budget_exceeded"},
		{"disk unsupported", `{"limits":{"disk_bytes":1048576}}`, runnerterminal.UnsupportedCapability, executionbudget.StateRejectedUnsupported, "unsupported_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &request))
			request["budgets"] = json.RawMessage(tc.budgets)
			data, err := json.Marshal(request)
			require.NoError(t, err)
			command := newRunnerCommandWithService(runnerunit.NewService(nil))
			command.SetArgs([]string{"run"})
			command.SetIn(bytes.NewReader(data))
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&bytes.Buffer{})
			err = command.ExecuteContext(t.Context())
			code, _ := tc.want.ExitCode()
			if code == 0 {
				require.NoError(t, err)
			} else {
				exit, ok := errors.AsType[*ExitCodeError](err)
				require.True(t, ok, "%v", err)
				require.Equal(t, code, exit.Code)
			}
			envelope, err := runnerterminal.Validate(stdout.Bytes(), nil)
			if tc.want != runnerterminal.UnsupportedCapability {
				// Admitted envelopes are validated with their binding elsewhere;
				// decode them here only to inspect the budget report.
				envelope = &runnerterminal.Envelope{}
				err = json.Unmarshal(stdout.Bytes(), envelope)
			}
			require.NoError(t, err, "%s", stdout.String())
			require.Equal(t, tc.want, envelope.Outcome)
			require.NotNil(t, envelope.Budget)
			require.Equal(t, tc.state, envelope.Budget.State)
			if tc.code != "" {
				codes := []string{}
				for _, diagnostic := range envelope.Diagnostics {
					codes = append(codes, diagnostic.Code)
				}
				require.Contains(t, codes, tc.code)
			}
		})
	}
}
