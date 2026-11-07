package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestAggregateProtectedSubmitOriginalContextPublicPaths(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, control := range []string{"success", "failed", "canceled", "missing-reservation", "missing-completion", "corrupt-completion", "mismatched-descriptor"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			storePath := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storePath, 0o700))
			backend, err := artifactmanifest.OpenLocal(storePath)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close()) }()
			raw, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			request, err := runnercontract.Parse(raw)
			require.NoError(t, err)
			if control == "failed" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- absent-control", 1)
				require.Contains(t, request.Input.Task, "absent-control")
			}
			raw, err = json.Marshal(request)
			require.NoError(t, err)
			wire, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
			})
			require.NoError(t, err)
			factories := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
			require.NoError(t, err)
			submitted, err := service.Submit(t.Context(), wire, backend, hooks)
			require.NoError(t, err)
			require.NotNil(t, submitted.Reservation)
			require.NotNil(t, submitted.Completion)
			require.NotNil(t, submitted.Terminal)
			source := executionattempt.Source{
				ExecutionID: submitted.Reservation.ExecutionID, ManifestDigest: submitted.Completion.ManifestDigest,
			}
			originalOrdinal := submitted.Terminal.Selection.Attempt
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			completed, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName, "execution-attempt.json")
			require.NoError(t, err)
			require.Equal(t, completed.Reservation.Bytes, completed.Publication.ArtifactBytes["execution-attempt.json"])
			index, err := evidencestate.ParseCapture(completed.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
			require.NoError(t, err)
			// Source is explicitly the actual Submit reservation/completion, not
			// inferred from index metadata or a synthetic second publication.
			document := evidencestate.Document{
				Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
				OriginManifest: source.ManifestDigest, ExecutionSource: &source, AttemptArtifact: "execution-attempt.json",
				Categories: index.Categories, Lineage: []evidencestate.Lineage{},
			}
			published, err := evidencestate.Publish(t.Context(), backend, document)
			require.NoError(t, err)
			request.Selection.Attempt++
			reuseRequest, err := json.Marshal(request)
			require.NoError(t, err)
			reuseWire, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: reuseRequest,
				Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source},
			})
			require.NoError(t, err)
			reused, err := service.Submit(t.Context(), reuseWire, backend, hooks)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, originalOrdinal, reused.Terminal.Selection.Attempt)
			require.Equal(t, submitted.Terminal.Outcome, reused.Terminal.Outcome)
			require.Equal(t, submitted.Completion, reused.Completion)
			if strings.HasPrefix(control, "missing-") {
				kind := strings.TrimPrefix(control, "missing-")
				path := filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-"+kind+"-"+source.ExecutionID)
				require.FileExists(t, path)
				require.NoError(t, os.Remove(path))
			}
			if control == "corrupt-completion" {
				path := filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+source.ExecutionID)
				require.FileExists(t, path)
				require.NoError(t, os.Chmod(path, 0o600))
				require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
			}
			if control == "mismatched-descriptor" {
				changed := completed.Reservation.Attempt
				changed.Parent = &executionattempt.Parent{ExecutionID: "exec-" + strings.Repeat("a", 64), Relationship: "branch"}
				attempt, err := executionattempt.EncodeAttempt(changed)
				require.NoError(t, err)
				completion := completed.Completion
				completion.ReservationDigest = artifactmanifest.Hash(attempt)
				completionBytes, err := executionledger.EncodeCompletion(completion)
				require.NoError(t, err)
				require.NoError(t, os.Chmod(filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-reservation-"+source.ExecutionID), 0o600))
				require.NoError(t, os.Chmod(filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+source.ExecutionID), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-reservation-"+source.ExecutionID), attempt, 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+source.ExecutionID), completionBytes, 0o600))
				_, err = ledger.ReadCompleted(t.Context(), source, "execution-attempt.json")
				require.NoError(t, err, "ordinary IDs/binding agree; only exact descriptor bytes detect this mutation")
			}
			pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: source.ManifestDigest, EvidenceSHA256: published.EvidenceSHA256}}
			body, err := json.Marshal(manifestaggregate.Request{Manifests: []string{source.ManifestDigest}, Evidence: &pairs})
			require.NoError(t, err)
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutes(mux, directory, nil)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(body)))
			expectedHTTP := http.StatusOK
			expectedState := "verified"
			if control == "canceled" {
				expectedHTTP = http.StatusConflict
			}
			switch control {
			case "missing-reservation", "missing-completion":
				expectedState, expectedHTTP = "missing", http.StatusConflict
			case "corrupt-completion":
				expectedState, expectedHTTP = "corrupt", http.StatusConflict
			case "mismatched-descriptor":
				expectedState, expectedHTTP = "conflicting", http.StatusConflict
			}
			require.Equal(t, expectedHTTP, response.Code, response.Body.String())
			var aggregate manifestaggregate.Report
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &aggregate))
			candidate := aggregate.Evidence.Members[0].Candidates[0]
			require.Equal(t, expectedState, candidate.SourceContext.State)
			require.Equal(t, source, *candidate.SourceContext.Source)
			reader, err := manifestaggregate.NewEvidenceReader(backend)
			require.NoError(t, err)
			inspection, _, categoryErr := reader.InspectCategories(t.Context(), published.EvidenceSHA256)
			require.NotNil(t, inspection.Document, "the actual owned sidecar supplies source context, not a hand-authored response")
			require.NoError(t, categoryErr)
			contextView, contextErr := reader.InspectSourceContext(t.Context(), *inspection.Document)
			require.Equal(t, candidate.SourceContext, contextView, "public read-only consumer and Aggregate retain the same source policy")
			if expectedState == "verified" {
				require.NoError(t, contextErr)
			} else {
				require.Error(t, contextErr)
			}
			require.Equal(t, submitted.Terminal.Outcome, aggregate.Members[0].Outcome)
			require.Equal(t, "withheld", aggregate.Evidence.UsageTotals)
			require.Equal(t, originalOrdinal, submitted.Terminal.Selection.Attempt)
			command := newAggregateCommand()
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetArgs([]string{"--store", storePath, "--evidence", source.ManifestDigest + "=" + published.EvidenceSHA256, source.ManifestDigest})
			commandErr := command.Execute()
			if expectedHTTP == http.StatusConflict {
				require.ErrorIs(t, commandErr, manifestaggregate.ErrIncomplete)
				require.Equal(t, "incomplete", aggregate.Completion)
			} else if control == "failed" {
				var exit *ExitCodeError
				require.ErrorAs(t, commandErr, &exit)
				require.Equal(t, ExitTestFailed, exit.Code)
			} else {
				require.NoError(t, commandErr)
			}
			require.Equal(t, response.Body.String(), strings.TrimSuffix(output.String(), "\n"))
			external := exec.CommandContext(t.Context(), binary, "aggregate", "--store", storePath,
				"--evidence", source.ManifestDigest+"="+published.EvidenceSHA256, source.ManifestDigest)
			external.Dir = directory
			external.Env = append(os.Environ(), "NO_COLOR=1", "ENABLE_COPILOT_TESTS=false",
				"ENABLE_LIVE_COPILOT_TESTS=false", "COPILOT_CLI_PATH=/usr/bin/false", "WAZA_NO_UPDATE_CHECK=1")
			output.Reset()
			external.Stdout = &output
			exitCode := ExitSuccess
			if expectedHTTP == http.StatusConflict {
				exitCode = ExitError
			} else if control == "failed" {
				exitCode = ExitTestFailed
			}
			externalErr := external.Run()
			if exitCode == ExitSuccess {
				require.NoError(t, externalErr)
			} else {
				var failure *exec.ExitError
				require.ErrorAs(t, externalErr, &failure)
				require.Equal(t, exitCode, failure.ExitCode())
			}
			require.Equal(t, response.Body.Bytes(), bytes.TrimSuffix(output.Bytes(), []byte("\n")),
				"the external CLI persists the same source-context failures and exit policy")
			csvResponse := httptest.NewRecorder()
			mux.ServeHTTP(csvResponse, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates?format=csv", bytes.NewReader(body)))
			require.Equal(t, expectedHTTP, csvResponse.Code)
			rows, err := csv.NewReader(csvResponse.Body).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 2)
			require.Equal(t, "source_context_json", rows[0][21])
			require.Contains(t, rows[1][21], `"state":"`+expectedState+`"`)
			require.Contains(t, rows[1][21], source.ExecutionID)
			if expectedState != "verified" {
				aggregate.Completion = "complete"
				_, err = manifestaggregate.Encode(aggregate)
				require.Error(t, err, "a failed declared source must not become success-shaped serialized output")
			}
			require.Equal(t, 1, factories, "no aggregation/CLI/API/CSV pass constructs an engine")
		})
	}
}
