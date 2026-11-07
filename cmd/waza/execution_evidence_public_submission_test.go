package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestExecutionEvidencePublicCLIAndHTTPSubmissionCaptureReuseAndRetry(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		failed   bool
		canceled bool
		exit     int
		outcome  runnerterminal.Outcome
	}{
		{name: "success", exit: 0, outcome: runnerterminal.Success},
		{name: "failed-original", failed: true, exit: 1, outcome: runnerterminal.EvaluationFailure},
		{name: "canceled-original", canceled: true, exit: 3, outcome: runnerterminal.Canceled},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			storeRoot := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storeRoot, 0o700))
			wire := checkpointCLIWire(t)
			changed := bytes.Replace(wire, []byte("prompt: hello"), []byte("prompt: hello public-private-evidence-control"), 1)
			require.NotEqual(t, wire, changed)
			wire = changed
			if scenario.failed {
				changed = bytes.Replace(wire, []byte("- hello"), []byte("- absent-public-evidence-control"), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
			}
			service := runnerunit.NewService(nil)
			if scenario.canceled {
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, context.Canceled
				})
			}
			original := runSubmissionWithService(t, service, storeRoot, wire)
			require.Equal(t, "completed", original.Status)
			require.Equal(t, scenario.exit, original.ExitCode)
			require.NotNil(t, original.Terminal)
			require.Equal(t, scenario.outcome, original.Terminal.Outcome)
			require.Equal(t, scenario.exit, original.Terminal.ExitCode)
			if scenario.canceled {
				require.Zero(t, original.EngineCalls)
			} else {
				require.Equal(t, 1, original.EngineCalls)
				require.NotNil(t, original.Terminal.Observation)
				require.NotEmpty(t, original.Terminal.Observation.Validations)
				hasFailure := false
				for _, verdict := range original.Terminal.Observation.Validations {
					hasFailure = hasFailure || !verdict.Passed
				}
				require.Equal(t, scenario.failed, hasFailure)
			}
			require.NotNil(t, original.Reservation)
			require.NotNil(t, original.Completion)
			require.Empty(t, original.Report)
			source := executionattempt.Source{
				ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
			}
			backend, err := artifactmanifest.OpenLocal(storeRoot)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			captured, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName, "execution-attempt.json", "terminal.json")
			require.NoError(t, err)
			index, err := evidencestate.ParseCapture(captured.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
			require.NoError(t, err)
			require.Equal(t, captured.Reservation.Bytes, captured.Publication.ArtifactBytes["execution-attempt.json"])
			for _, artifact := range captured.Publication.Manifest.Artifacts {
				require.NotEqual(t, "report.json", artifact.Name)
				data, err := backend.Get(t.Context(), "blob-"+artifact.SHA256, artifactmanifest.MaxArtifactBytes)
				require.NoError(t, err)
				require.NotContains(t, string(data), "public-private-evidence-control")
			}
			document := evidencestate.Document{
				Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
				OriginManifest: source.ManifestDigest, ExecutionSource: &source, AttemptArtifact: "execution-attempt.json",
				Categories: index.Categories, Lineage: []evidencestate.Lineage{},
			}
			data, err := evidencestate.Encode(document)
			require.NoError(t, err)
			input := filepath.Join(t.TempDir(), "evidence.json")
			require.NoError(t, os.WriteFile(input, data, 0o600))
			published, err := runArtifactCLI(t, directory, "evidence", "publish", input)
			require.NoError(t, err)
			var receipt evidencestate.Inspection
			require.NoError(t, json.Unmarshal(published, &receipt))
			mux := http.NewServeMux()
			webapi.RegisterSubmissionRoutes(mux, directory, nil)
			webapi.RegisterEvidenceRoutesWithBackend(mux, backend)
			server := httptest.NewServer(mux)
			defer server.Close()
			client := server.Client()
			client.Timeout = 10 * time.Second
			post := func(request runnerunit.SubmissionRequest) runnerunit.Submission {
				t.Helper()
				body, err := json.Marshal(request)
				require.NoError(t, err)
				httpRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/runner/submit", bytes.NewReader(body))
				require.NoError(t, err)
				httpRequest.Header.Set("Content-Type", "application/json")
				response, err := client.Do(httpRequest)
				require.NoError(t, err)
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusOK, response.StatusCode, "%s", data)
				result, err := runnerunit.ParseSubmissionResult(data)
				require.NoError(t, err, "%s", data)
				require.Empty(t, result.Report)
				require.NotContains(t, string(data), "public-private-evidence-control")
				return *result
			}
			request, err := runnerunit.ParseSubmission(wire)
			require.NoError(t, err)
			changed = bytes.Replace(request.Request, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.NotEqual(t, request.Request, changed)
			request.Request = changed
			request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source}
			reused := post(*request)
			require.Equal(t, "reused", reused.Status)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, original.ExitCode, reused.ExitCode)
			require.Equal(t, original.Terminal, reused.Terminal)
			require.Equal(t, original.Reservation, reused.Reservation)
			after, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName, "execution-attempt.json", "terminal.json")
			require.NoError(t, err)
			require.Equal(t, captured.Publication.ManifestBytes, after.Publication.ManifestBytes)
			require.Equal(t, captured.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
			output, err := runArtifactCLI(t, directory, "evidence", "categories", receipt.EvidenceSHA256)
			require.NoError(t, err)
			var categories struct {
				Categories []evidencestate.CategoryEvidence `json:"categories"`
			}
			require.NoError(t, json.Unmarshal(output, &categories))
			require.Len(t, categories.Categories, 6)
			parserContext, cancelParser := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancelParser()
			node := exec.CommandContext(parserContext, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseRecordedCategories } from "../../web/src/api/executionEvidenceCategories.ts";
const text = readFileSync(0, "utf8");
const result = parseRecordedCategories(text, JSON.parse(text).evidence_sha256, 200);
assert.equal(result.sourceVerified, true);
assert.equal(result.admissionComplete, true);
assert.equal(result.categories.length, 6);
for (const category of result.categories) {
  if (category.state === "redacted" || category.state === "unavailable") {
    assert.equal(category.observed_count, undefined);
    assert.equal(category.usage, undefined);
  }
}
`)
			node.Stdin = bytes.NewReader(output)
			parserOutput, err := node.CombinedOutput()
			require.NoError(t, err, "%s", parserOutput)
			for _, suffix := range []string{"", "/document", "/csv"} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+receipt.EvidenceSHA256+"/categories"+suffix, nil))
				require.Equal(t, http.StatusOK, response.Code)
				require.NotContains(t, response.Body.String(), "public-private-evidence-control")
				if suffix != "/csv" {
					require.JSONEq(t, string(output), response.Body.String())
				} else {
					rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
					require.NoError(t, err)
					require.Len(t, rows, 7)
					for i, category := range categories.Categories {
						require.Len(t, rows[i+1], 18)
						require.Equal(t, string(category.Category), rows[i+1][0])
						require.Equal(t, string(category.State), rows[i+1][2])
						if category.State == evidencestate.Redacted || category.State == evidencestate.Unavailable {
							require.Empty(t, rows[i+1][5])
							for _, value := range rows[i+1][6:13] {
								require.Empty(t, value)
							}
						}
					}
				}
			}
			request.Policy = &runnerunit.SubmissionPolicy{
				Mode: "distinct", Parent: &executionattempt.Parent{ExecutionID: source.ExecutionID, Relationship: "retry"},
			}
			retry := post(*request)
			require.Equal(t, map[bool]int{false: 0, true: 1}[scenario.failed], retry.ExitCode)
			require.NotEqual(t, source.ExecutionID, retry.Reservation.ExecutionID)
			require.Equal(t, 1, retry.EngineCalls)
			child := executionattempt.Source{ExecutionID: retry.Reservation.ExecutionID, ManifestDigest: retry.Completion.ManifestDigest}
			counter := &retryReadCounter{}
			reader := retryFixtureReader(t, backend, counter)
			state, parent, err := evidencestate.InspectRetryParent(t.Context(), reader, child, "execution-attempt.json")
			require.NoError(t, err)
			require.Equal(t, evidencestate.Present, state)
			require.Equal(t, &evidencestate.Lineage{Manifest: source.ManifestDigest, Relationship: evidencestate.Retry}, parent)
		})
	}
}
