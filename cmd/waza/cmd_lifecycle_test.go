package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func lifecycleCLIFixture(t *testing.T) (string, string, executionlifecycle.Inspection, []byte, runnerterminal.Envelope) {
	t.Helper()
	directory := t.TempDir()
	storeDir := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storeDir, 0o700))
	data, err := os.ReadFile("../../internal/runnercontract/testdata/request.json")
	require.NoError(t, err)
	terminal, binding := runnerunit.NewService(nil).RunTerminal(t.Context(), data)
	require.NotNil(t, binding)
	require.Equal(t, "success", string(terminal.Outcome))
	attempt := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical: executionattempt.LogicalFrom(*binding.Identity), ExecutionID: "exec-" + strings.Repeat("a", 64),
		Binding: artifactmanifest.Binding{
			Identity: *binding.Identity, Selection: binding.Selection,
			ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
		},
	}
	reservation, err := executionattempt.EncodeAttempt(attempt)
	require.NoError(t, err)
	local, err := artifactmanifest.OpenLocal(storeDir)
	require.NoError(t, err)
	reservationDigest := artifactmanifest.Hash(reservation)
	require.NoError(t, artifactmanifest.NewStore(local).Put(t.Context(), artifactmanifest.Artifact{
		Name: "reservation", SHA256: reservationDigest, SizeBytes: int64(len(reservation)), MediaType: "application/json",
	}, reservation))
	observations := []executionlifecycle.Event{
		{Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
			ExecutionID: attempt.ExecutionID, ReservationDigest: reservationDigest,
			Sequence: 1, ObservedAt: "2026-11-07T17:00:00Z", Event: "started"},
		{Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
			ExecutionID: attempt.ExecutionID, ReservationDigest: reservationDigest,
			Sequence: 2, ObservedAt: "2026-11-07T17:00:00Z", Event: "heartbeat"},
	}
	inspection := executionlifecycle.Inspection{
		Kind: "waza.execution-lifecycle-inspection", SchemaVersion: executionlifecycle.Version,
		OriginalAdmission: attempt.Binding, ReservationDigest: reservationDigest, ObservationDigests: []string{},
		AsOf: "2026-11-07T17:01:00Z", StaleAfterMS: 60000,
	}
	for _, observation := range observations {
		digest, err := executionlifecycle.NewBlobs(local).Put(t.Context(), observation)
		require.NoError(t, err)
		inspection.ObservationDigests = append(inspection.ObservationDigests, digest)
	}
	require.NoError(t, local.Close())
	contextJSON, err := json.Marshal(inspection)
	require.NoError(t, err)
	contextPath := filepath.Join(directory, "inspection.json")
	require.NoError(t, os.WriteFile(contextPath, contextJSON, 0o600))
	return directory, contextPath, inspection, reservation, terminal
}

func TestLifecycleActualCLIToConfiguredAPI(t *testing.T) {
	directory, contextPath, _, reservation, _ := lifecycleCLIFixture(t)
	cmd := newRootCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"lifecycle", "--store", filepath.Join(directory, ".waza-artifacts"), "inspect", contextPath})
	require.NoError(t, cmd.Execute())
	var result executionlifecycle.Snapshot
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, "caller-declared-unverified", result.Evidence)
	require.Equal(t, "running", result.State)
	require.Len(t, result.Observations, 2)
	require.True(t, *result.SuspectedOrphan)
	require.Equal(t, 0, *result.CompletedUnits)
	require.Nil(t, result.Terminal)
	contextJSON, err := os.ReadFile(contextPath)
	require.NoError(t, err)
	mux := http.NewServeMux()
	webapi.RegisterLifecycleRoutes(mux, directory, nil)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	response, err := server.Client().Post(server.URL+"/api/v1/lifecycle/inspect", "application/json", bytes.NewReader(contextJSON))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.JSONEq(t, output.String(), string(body))
	retained, err := os.ReadFile(filepath.Join(directory, ".waza-artifacts", "blob-"+artifactmanifest.Hash(reservation)))
	require.NoError(t, err)
	require.Equal(t, reservation, retained, "inspection must not replace original evidence")
	fixture, err := os.ReadFile("../../web/e2e/fixtures/lifecycle.json")
	require.NoError(t, err)
	var browser struct {
		Context    json.RawMessage `json:"context"`
		Projection json.RawMessage `json:"projection"`
	}
	require.NoError(t, json.Unmarshal(fixture, &browser))
	require.JSONEq(t, string(browser.Context), string(contextJSON), "browser context must match actual admitted producer context")
	require.JSONEq(t, string(browser.Projection), string(body), "browser projection must be emitted by the real configured API")
}

func TestLifecycleInspectionErrorsDoNotProduceSuccess(t *testing.T) {
	directory, _, inspection, _, _ := lifecycleCLIFixture(t)
	mux := http.NewServeMux()
	webapi.RegisterLifecycleRoutes(mux, directory, nil)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	for _, control := range []struct {
		name   string
		status int
		change func(*executionlifecycle.Inspection)
	}{
		{"missing", http.StatusNotFound, func(i *executionlifecycle.Inspection) { i.ReservationDigest = strings.Repeat("b", 64) }},
		{"context", http.StatusConflict, func(i *executionlifecycle.Inspection) { i.OriginalAdmission.Selection.Attempt++ }},
		{"order", http.StatusConflict, func(i *executionlifecycle.Inspection) {
			i.ObservationDigests[0], i.ObservationDigests[1] = i.ObservationDigests[1], i.ObservationDigests[0]
		}},
		{"invalid", http.StatusBadRequest, func(i *executionlifecycle.Inspection) { i.StaleAfterMS = 0 }},
		{"malformed-stored-observation", http.StatusConflict, func(i *executionlifecycle.Inspection) {
			data := []byte("{}")
			digest := artifactmanifest.Hash(data)
			path := filepath.Join(directory, ".waza-artifacts", "blob-"+digest)
			require.NoError(t, os.WriteFile(path, data, 0o600))
			retained, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, digest, artifactmanifest.Hash(retained), "bad profile has the requested content digest")
			i.ObservationDigests = []string{digest}
		}},
	} {
		t.Run(control.name, func(t *testing.T) {
			value := inspection
			value.ObservationDigests = append([]string{}, inspection.ObservationDigests...)
			control.change(&value)
			data, err := json.Marshal(value)
			require.NoError(t, err)
			response, err := server.Client().Post(server.URL+"/api/v1/lifecycle/inspect", "application/json", bytes.NewReader(data))
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, control.status, response.StatusCode)
			require.NotContains(t, string(body), `"kind":"waza.execution-lifecycle"`)
			require.NotContains(t, string(body), directory)
		})
	}
	cmd := newRootCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	invalid := filepath.Join(directory, "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("{}"), 0o600))
	cmd.SetArgs([]string{"lifecycle", "--store", filepath.Join(directory, "missing-store"), "inspect", invalid})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "inspection schema", "invalid input rejected before provider construction")
	require.Empty(t, output.String())
}

func TestLifecycleHelpAndOutputFailure(t *testing.T) {
	help := newRootCommand()
	var output bytes.Buffer
	help.SetOut(&output)
	help.SetErr(io.Discard)
	help.SetArgs([]string{"lifecycle", "inspect", "--help"})
	require.NoError(t, help.Execute())
	require.Contains(t, output.String(), "inspect FILE.json")
	require.Contains(t, output.String(), "--store")
	require.Contains(t, output.String(), "--azure-container-url")

	directory, path, _, _, _ := lifecycleCLIFixture(t)
	cmd := newRootCommand()
	cmd.SetOut(errorWriter{})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"lifecycle", "inspect", path, "--store", filepath.Join(directory, ".waza-artifacts")})
	err := cmd.Execute()
	require.ErrorContains(t, err, "writing lifecycle inspection")
	var exit *ExitCodeError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 2, exit.Code)
}

func TestLifecycleTerminalBrowserFixturesThroughActualAPI(t *testing.T) {
	type terminalFixture struct {
		ObservationDigest string                         `json:"observation_digest"`
		Terminal          *executionlifecycle.Terminal   `json:"terminal"`
		CompletedUnits    *int                           `json:"completed_units,omitempty"`
		Retry             executionlifecycle.RetryAdvice `json:"retry"`
		Actions           []string                       `json:"actions"`
	}
	baseline, err := os.ReadFile("../../web/e2e/fixtures/lifecycle.json")
	require.NoError(t, err)
	var browser struct {
		Projection executionlifecycle.Snapshot `json:"projection"`
	}
	require.NoError(t, json.Unmarshal(baseline, &browser))
	actual := make(map[string]terminalFixture)
	for _, name := range []string{"success", "infrastructure_failure"} {
		directory, _, inspection, reservation, terminal := lifecycleCLIFixture(t)
		if name == "infrastructure_failure" {
			data, err := os.ReadFile("../../internal/runnercontract/testdata/request.json")
			require.NoError(t, err)
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				return nil, errors.New("controlled lifecycle fixture factory failure")
			})
			produced, binding := service.RunTerminal(t.Context(), data)
			require.NotNil(t, binding)
			require.Equal(t, inspection.OriginalAdmission, artifactmanifest.Binding{
				Identity: *binding.Identity, Selection: binding.Selection,
				ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
			})
			terminal = produced
		}
		require.Equal(t, name, string(terminal.Outcome))
		attempt, err := executionattempt.ParseAttempt(reservation)
		require.NoError(t, err)
		event := executionlifecycle.Event{
			Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
			ExecutionID: attempt.ExecutionID, ReservationDigest: inspection.ReservationDigest,
			Sequence: 3, ObservedAt: inspection.AsOf, Event: "terminal_recorded", Terminal: &terminal,
		}
		backend, err := artifactmanifest.OpenLocal(filepath.Join(directory, ".waza-artifacts"))
		require.NoError(t, err)
		digest, err := executionlifecycle.NewBlobs(backend).Put(t.Context(), event)
		require.NoError(t, err)
		require.NoError(t, backend.Close())
		inspection.ObservationDigests = append(inspection.ObservationDigests, digest)
		contextJSON, err := json.Marshal(inspection)
		require.NoError(t, err)
		mux := http.NewServeMux()
		webapi.RegisterLifecycleRoutes(mux, directory, nil)
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		response, err := server.Client().Post(server.URL+"/api/v1/lifecycle/inspect", "application/json", bytes.NewReader(contextJSON))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var snapshot executionlifecycle.Snapshot
		require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
		require.NoError(t, response.Body.Close())
		server.Close()
		require.Equal(t, "terminal-recorded", snapshot.State)
		require.Equal(t, "caller-declared-unverified", snapshot.Evidence)
		expectedActions := []string{"inspect-original-evidence"}
		if name == "infrastructure_failure" {
			expectedActions = append(expectedActions, "review-independent-fresh-attempt")
		}
		require.Equal(t, expectedActions, snapshot.Actions)
		require.False(t, *snapshot.SuspectedOrphan, "recorded terminal suppresses stale-heartbeat suspicion")
		require.Len(t, snapshot.Observations, 3)
		require.Equal(t, "terminal_recorded", snapshot.Observations[2].Event)
		require.Equal(t, inspection.AsOf, snapshot.Observations[2].ObservedAt)
		original, err := runnerterminal.Encode(terminal, inspection.OriginalAdmission.TerminalBinding())
		require.NoError(t, err)
		require.Equal(t, artifactmanifest.Hash(original), snapshot.Terminal.Digest)
		eventBytes, err := os.ReadFile(filepath.Join(directory, ".waza-artifacts", "blob-"+digest))
		require.NoError(t, err)
		retainedEvent, err := executionlifecycle.Parse(eventBytes)
		require.NoError(t, err)
		retainedTerminal, err := runnerterminal.Encode(*retainedEvent.Terminal, inspection.OriginalAdmission.TerminalBinding())
		require.NoError(t, err)
		require.Equal(t, original, retainedTerminal, "inspection must preserve the full original terminal")
		retained, err := os.ReadFile(filepath.Join(directory, ".waza-artifacts", "blob-"+artifactmanifest.Hash(reservation)))
		require.NoError(t, err)
		require.Equal(t, reservation, retained)
		expectedProjection := browser.Projection
		expectedProjection.State = "terminal-recorded"
		expectedProjection.Terminal = snapshot.Terminal
		expectedProjection.CompletedUnits = snapshot.CompletedUnits
		expectedProjection.Retry = snapshot.Retry
		expectedProjection.Actions = expectedActions
		expectedProjection.SuspectedOrphan = new(false)
		expectedProjection.Observations = append(append([]executionlifecycle.Observation{}, expectedProjection.Observations...),
			executionlifecycle.Observation{Sequence: 3, ObservedAt: inspection.AsOf, Event: "terminal_recorded"})
		require.Equal(t, expectedProjection, snapshot, "browser terminal variant must match the complete actual API projection")
		actual[name] = terminalFixture{
			ObservationDigest: digest, Terminal: snapshot.Terminal,
			CompletedUnits: snapshot.CompletedUnits, Retry: snapshot.Retry, Actions: snapshot.Actions,
		}
	}
	data, err := json.Marshal(actual)
	require.NoError(t, err)
	expected, err := os.ReadFile("../../web/e2e/fixtures/lifecycle-terminal.json")
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(data))
}
