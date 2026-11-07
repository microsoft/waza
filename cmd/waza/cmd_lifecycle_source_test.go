package main

import (
	"bytes"
	"context"
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
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestLifecycleSourceActualProtectedCLIConfiguredAPIAndSourceDamage(t *testing.T) {
	type browserCase struct {
		Control string `json:"control"`
		Query   string `json:"query"`
		JSON    string `json:"json"`
	}
	var browserCases []browserCase
	for _, control := range []string{"success", "failed", "canceled"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			storeDir := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storeDir, 0o700))
			backend, err := artifactmanifest.OpenLocal(storeDir)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close()) }()
			data, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			wantOutcome, wantCode := runnerterminal.Success, 0
			if control == "failed" {
				changed := bytes.Replace(data, []byte("- hello"), []byte("- private-lifecycle-source-nearmiss"), 1)
				require.NotEqual(t, data, changed)
				data = changed
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			}
			calls := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				calls++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			if control == "canceled" {
				wantOutcome, wantCode = runnerterminal.Canceled, 3
			}
			submission, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
			})
			require.NoError(t, err)
			created, err := ownedsubmission.SubmitCaptured(t.Context(), service, submission, backend)
			require.NoError(t, err)
			require.Equal(t, wantOutcome, created.Terminal.Outcome)
			require.Equal(t, wantCode, created.ExitCode)
			require.Equal(t, 1, calls)
			source := executionattempt.Source{ExecutionID: created.Reservation.ExecutionID, ManifestDigest: created.Completion.ManifestDigest}
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			original, err := ledger.ReadCompleted(t.Context(), source, "lifecycle.json", "terminal.json")
			require.NoError(t, err)
			captureBytes := original.Publication.ArtifactBytes["lifecycle.json"]
			require.NotEmpty(t, captureBytes)
			capturePath := filepath.Join(storeDir, "blob-"+artifactmanifest.Hash(captureBytes))
			query := ownedsubmission.LifecycleSourceRequest{
				Kind: "waza.execution-lifecycle-source-inspection", SchemaVersion: "1.0",
				Source: source, StaleAfterMS: 1000,
			}
			queryJSON, err := json.Marshal(query)
			require.NoError(t, err)
			queryPath := filepath.Join(directory, "source.json")
			require.NoError(t, os.WriteFile(queryPath, queryJSON, 0o600))
			execute := func() ([]byte, error) {
				t.Helper()
				cmd := newRootCommand()
				cmd.SetArgs([]string{"lifecycle", "source", queryPath, "--store", storeDir})
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(io.Discard)
				err := cmd.ExecuteContext(t.Context())
				return bytes.Clone(output.Bytes()), err
			}
			output, err := execute()
			require.NoError(t, err, "inspection succeeds independently of original terminal exit")
			var inspected ownedsubmission.LifecycleInspection
			require.NoError(t, json.Unmarshal(output, &inspected))
			require.Equal(t, source, inspected.Source)
			require.Equal(t, wantOutcome, inspected.Snapshot.Terminal.Outcome)
			require.Equal(t, wantCode, inspected.Snapshot.Terminal.ExitCode)
			require.Equal(t, "unknown", inspected.Runtime.ProcessExit)
			require.Equal(t, "caller-declared-unverified", inspected.Snapshot.Evidence)
			require.Equal(t, "verified-nonsnapshot-content", inspected.Verification)
			require.NotContains(t, string(output), "private-lifecycle-source-nearmiss")
			require.NotContains(t, string(output), `"report"`)
			mux := http.NewServeMux()
			webapi.RegisterLifecycleRoutes(mux, directory, nil)
			server := httptest.NewServer(mux)
			defer server.Close()
			post := func(query []byte) (int, []byte) {
				t.Helper()
				response, err := server.Client().Post(server.URL+"/api/v1/lifecycle/source", "application/json", bytes.NewReader(query))
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				return response.StatusCode, body
			}
			status, body := post(queryJSON)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, output, body, "configured API and actual CLI export the same source bytes")
			browserCases = append(browserCases, browserCase{Control: control, Query: string(queryJSON), JSON: string(body)})
			require.Equal(t, 1, calls, "inspection never constructs an engine")
			conflicting := query
			conflicting.Source.ManifestDigest = strings.Repeat("0", 64)
			require.NotEqual(t, query.Source, conflicting.Source)
			conflictingJSON, err := json.Marshal(conflicting)
			require.NoError(t, err)
			status, body = post(conflictingJSON)
			require.Equal(t, http.StatusConflict, status)
			require.NotContains(t, string(body), `"snapshot"`)
			require.NoError(t, os.WriteFile(queryPath, conflictingJSON, 0o600))
			conflictOutput, conflictErr := execute()
			conflictExit, ok := errors.AsType[*ExitCodeError](conflictErr)
			require.True(t, ok, "%v", conflictErr)
			require.Equal(t, 2, conflictExit.Code)
			require.Empty(t, conflictOutput)
			require.NoError(t, os.WriteFile(queryPath, queryJSON, 0o600))
			budgeted, err := manifestaggregate.NewEvidenceReader(backend)
			require.NoError(t, err)
			exhausted := false
			for range 512 {
				_, _, err := budgeted.InspectCategories(t.Context(), strings.Repeat("0", 64))
				if errors.Is(err, manifestaggregate.ErrLimit) {
					exhausted = true
					break
				}
				require.ErrorIs(t, err, artifactmanifest.ErrMissing)
			}
			require.True(t, exhausted, "actual same private read budget must be exhausted")
			limitedMux := http.NewServeMux()
			webapi.RegisterLifecycleSourceRoutesWithInspector(limitedMux, budgeted)
			limitedServer := httptest.NewServer(limitedMux)
			for range 2 {
				response, err := limitedServer.Client().Post(limitedServer.URL+"/api/v1/lifecycle/source", "application/json", bytes.NewReader(queryJSON))
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
				require.NotContains(t, string(body), `"snapshot"`)
				require.NotContains(t, string(body), directory)
			}
			limitedServer.Close()
			require.Equal(t, 1, calls, "limit denials never create a fresh reader budget or engine")
			for _, damage := range []string{"same-size-corrupt", "missing"} {
				if damage == "same-size-corrupt" {
					corrupt := bytes.Clone(captureBytes)
					corrupt[len(corrupt)-1] ^= 1
					require.Len(t, corrupt, len(captureBytes))
					require.NotEqual(t, captureBytes, corrupt)
					require.NoError(t, os.Chmod(capturePath, 0o600))
					require.NoError(t, os.WriteFile(capturePath, corrupt, 0o600))
				} else {
					require.NoError(t, os.Remove(capturePath))
				}
				output, err := execute()
				exit, ok := errors.AsType[*ExitCodeError](err)
				require.True(t, ok, "%v", err)
				require.Equal(t, 2, exit.Code)
				require.Empty(t, output)
				status, body := post(queryJSON)
				require.Equal(t, map[string]int{"same-size-corrupt": 409, "missing": 404}[damage], status)
				require.NotContains(t, string(body), `"snapshot"`)
				require.NotContains(t, string(body), directory)
				require.Equal(t, 1, calls)
				manifestBytes, err := os.ReadFile(filepath.Join(storeDir, "manifest-"+source.ManifestDigest))
				require.NoError(t, err)
				require.Equal(t, original.Publication.ManifestBytes, manifestBytes)
				if damage == "same-size-corrupt" {
					require.NoError(t, os.WriteFile(capturePath, captureBytes, 0o600))
				}
			}
			invalid := bytes.Replace(queryJSON, []byte(`"stale_after_ms":1000`), []byte(`"stale_after_ms":0`), 1)
			require.NotEqual(t, queryJSON, invalid)
			status, body = post(invalid)
			require.Equal(t, http.StatusBadRequest, status)
			require.NotContains(t, string(body), `"snapshot"`)
			require.Equal(t, 1, calls)
		})
	}
	if path := os.Getenv("WAZA_LIFECYCLE_SOURCE_FIXTURE"); path != "" {
		require.Len(t, browserCases, 3)
		data, err := json.Marshal(browserCases)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
	}
}
