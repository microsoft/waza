package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestArtifactEvidenceCategoriesFromSameProtectedSubmissionAndReuse(t *testing.T) {
	for _, name := range []string{"success", "grader-failure", "canceled"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			request, err := runnercontract.Parse(raw)
			require.NoError(t, err)
			request.Input.Task = strings.Replace(request.Input.Task, "prompt: hello", "prompt: hello withheld-source-control", 1)
			require.Contains(t, request.Input.Task, "withheld-source-control")
			wantCode := 0
			if name == "grader-failure" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- unmatched-grader-control", 1)
				require.Contains(t, request.Input.Task, "unmatched-grader-control")
				wantCode = 1
			}
			submissionWire := func(source *executionattempt.Source) []byte {
				t.Helper()
				wire, err := json.Marshal(request)
				require.NoError(t, err)
				wrapper := runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: wire}
				if source != nil {
					wrapper.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: source}
				}
				data, err := json.Marshal(wrapper)
				require.NoError(t, err)
				return data
			}
			directory := t.TempDir()
			storeDirectory := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storeDirectory, 0o700))
			backend, err := artifactmanifest.OpenLocal(storeDirectory)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
			require.NoError(t, err)
			service := runnerunit.NewService(nil)
			if name == "canceled" {
				wantCode = 3
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, context.Canceled
				})
			}
			result, err := service.Submit(t.Context(), submissionWire(nil), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, result.ExitCode)
			if name == "canceled" {
				require.Zero(t, result.EngineCalls)
			} else {
				require.Equal(t, 1, result.EngineCalls)
			}
			require.Empty(t, result.Report)
			require.NotNil(t, result.Completion)
			require.NotNil(t, result.Reservation)
			source := executionattempt.Source{
				ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest,
			}
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			completed, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName, "execution-attempt.json", "terminal.json")
			require.NoError(t, err)
			originalManifest := append([]byte(nil), completed.Publication.ManifestBytes...)
			originalTerminal := append([]byte(nil), completed.Publication.ArtifactBytes["terminal.json"]...)
			index, err := evidencestate.ParseCapture(completed.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
			require.NoError(t, err)
			require.Equal(t, result.Reservation.Binding, index.Binding)
			require.Equal(t, result.Completion.ReservationDigest, artifactmanifest.Hash(completed.Publication.ArtifactBytes["execution-attempt.json"]))
			for _, artifact := range completed.Publication.Manifest.Artifacts {
				require.NotEqual(t, "report.json", artifact.Name)
			}
			for _, data := range completed.Publication.ArtifactBytes {
				require.NotContains(t, string(data), "withheld-source-control")
			}
			document := evidencestate.Document{
				Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
				OriginManifest: source.ManifestDigest, ExecutionSource: &source, AttemptArtifact: "execution-attempt.json",
				Categories: index.Categories, Lineage: []evidencestate.Lineage{},
			}
			documentBytes, err := evidencestate.Encode(document)
			require.NoError(t, err)
			input := filepath.Join(t.TempDir(), "evidence.json")
			require.NoError(t, os.WriteFile(input, documentBytes, 0o600))
			publication, err := runArtifactCLI(t, directory, "evidence", "publish", input)
			require.NoError(t, err)
			var receipt evidencestate.Inspection
			require.NoError(t, json.Unmarshal(publication, &receipt))
			require.NotEqual(t, source.ManifestDigest, receipt.EvidenceSHA256)
			output, err := runArtifactCLI(t, directory, "evidence", "categories", receipt.EvidenceSHA256)
			require.NoError(t, err)
			var categoryReceipt struct {
				Categories []evidencestate.CategoryEvidence `json:"categories"`
			}
			require.NoError(t, json.Unmarshal(output, &categoryReceipt))
			require.Len(t, categoryReceipt.Categories, 6)
			for _, category := range categoryReceipt.Categories {
				switch category.Category {
				case evidencestate.Conversation, evidencestate.Tool, evidencestate.Reliability:
					require.Equal(t, evidencestate.Redacted, category.State)
					require.Nil(t, category.ObservedCount)
				case evidencestate.Usage:
					require.Equal(t, evidencestate.Unavailable, category.State)
					require.Nil(t, category.Usage)
				case evidencestate.Grader:
					if name == "canceled" {
						require.Equal(t, evidencestate.Unavailable, category.State)
						require.Nil(t, category.ObservedCount)
					} else {
						require.Equal(t, evidencestate.Present, category.State)
						require.NotNil(t, category.ObservedCount)
					}
				case evidencestate.Provenance:
					require.Equal(t, evidencestate.Present, category.State)
					require.NotNil(t, category.ObservedCount)
				default:
					t.Fatalf("unexpected category %q", category.Category)
				}
			}
			mux := http.NewServeMux()
			webapi.RegisterEvidenceRoutesWithBackend(mux, backend)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+receipt.EvidenceSHA256+"/categories", nil))
			require.Equal(t, http.StatusOK, response.Code)
			require.JSONEq(t, string(output), response.Body.String())
			require.NotContains(t, response.Body.String(), "withheld-source-control")
			require.NotContains(t, response.Body.String(), `"report":`)

			request.Selection.Attempt++
			reused, err := service.Submit(t.Context(), submissionWire(&source), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, reused.ExitCode, "evidence withholding must not rewrite the original native verdict")
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.Reservation.ExecutionID, reused.Reservation.ExecutionID)
			require.Equal(t, result.Completion.ManifestDigest, reused.Completion.ManifestDigest)
			require.Empty(t, reused.Report)
			encoded, err := runnerterminal.Encode(*reused.Terminal, result.Reservation.Binding.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, originalTerminal, encoded, "alternate caller ordinal must not retag the original terminal")
			after, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName, "execution-attempt.json", "terminal.json")
			require.NoError(t, err)
			require.Equal(t, originalManifest, after.Publication.ManifestBytes)
			require.Equal(t, completed.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
			var captureDigest string
			for _, artifact := range completed.Publication.Manifest.Artifacts {
				if artifact.Name == evidencestate.CaptureArtifactName {
					captureDigest = artifact.SHA256
				}
			}
			require.NotEmpty(t, captureDigest)
			require.NoError(t, os.Remove(filepath.Join(storeDirectory, "blob-"+captureDigest)))
			output, err = runArtifactCLI(t, directory, "evidence", "categories", receipt.EvidenceSHA256)
			require.Error(t, err)
			categoryReceipt.Categories = nil
			require.NoError(t, json.Unmarshal(output, &categoryReceipt))
			require.Len(t, categoryReceipt.Categories, 6)
			for _, category := range categoryReceipt.Categories {
				require.Equal(t, evidencestate.Unavailable, category.State)
				require.Nil(t, category.ObservedCount)
				require.Nil(t, category.Usage)
			}
			response = httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+receipt.EvidenceSHA256+"/categories", nil))
			require.Equal(t, http.StatusNotFound, response.Code)
			require.JSONEq(t, string(output), response.Body.String())
		})
	}
}
