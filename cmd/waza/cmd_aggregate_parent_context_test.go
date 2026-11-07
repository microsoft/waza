package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
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
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestAggregateParentBrowserFixtureProductionAPI(t *testing.T) {
	directory := t.TempDir()
	store := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(store, 0o700))
	var firstParent string
	var retryBody []byte
	for _, mode := range []string{"retry", "missing-parent"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), "go", "run",
				"../../internal/manifestaggregate/testdata/protected-producer", store,
				"../../examples/runner/unit.json", mode)
			wire, err := command.CombinedOutput()
			require.NoError(t, err, string(wire))
			var source struct {
				ManifestSHA256 string                  `json:"manifest_sha256"`
				EvidenceSHA256 string                  `json:"evidence_sha256"`
				ExecutionID    string                  `json:"execution_id"`
				Parent         executionattempt.Source `json:"parent"`
			}
			require.NoError(t, json.Unmarshal(wire, &source))
			require.NotEqual(t, source.ExecutionID, source.Parent.ExecutionID)
			if mode == "retry" {
				firstParent = source.Parent.ExecutionID
			} else {
				require.NotEqual(t, firstParent, source.Parent.ExecutionID, "distinct submissions create new actual execution IDs even for the same logical input")
			}
			pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: source.ManifestSHA256, EvidenceSHA256: source.EvidenceSHA256}}
			body, err := json.Marshal(manifestaggregate.Request{Manifests: []string{source.ManifestSHA256}, Evidence: &pairs})
			require.NoError(t, err)
			if mode == "retry" {
				retryBody = body
			}
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutes(mux, directory, nil)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(body)))
			expectedHTTP, expectedState := http.StatusOK, "verified"
			if mode == "missing-parent" {
				expectedHTTP, expectedState = http.StatusConflict, "unavailable"
			}
			require.Equal(t, expectedHTTP, response.Code, response.Body.String())
			var report manifestaggregate.Report
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
			candidate := report.Evidence.Members[0].Candidates[0]
			require.Equal(t, "verified", candidate.State)
			require.Len(t, candidate.Categories, 6)
			require.Equal(t, "verified", candidate.SourceContext.State)
			require.Equal(t, expectedState, candidate.SourceContext.ParentContext.State)
			if mode == "retry" {
				require.Equal(t, source.Parent, *candidate.SourceContext.ParentContext.Source)
			} else {
				require.Nil(t, candidate.SourceContext.ParentContext.Source)
			}
		})
	}
	require.NotEmpty(t, retryBody)
	require.NotEmpty(t, firstParent)
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutes(mux, directory, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(retryBody)))
	require.Equal(t, http.StatusOK, response.Code, "missing-parent control must not delete the separate retry fixture's parent")
	var report manifestaggregate.Report
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	parent := report.Evidence.Members[0].Candidates[0].SourceContext.ParentContext
	require.Equal(t, "verified", parent.State)
	require.Equal(t, firstParent, parent.Source.ExecutionID)
}

func TestAggregateParentContextActualProducerPublicContracts(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, control := range []string{"verified", "missing", "corrupt", "conflicting", "branch", "failed", "canceled", "no-parent", "primary-missing"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			storePath := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storePath, 0o700))
			backend, err := artifactmanifest.OpenLocal(storePath)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close()) }()
			raw, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			parentRequest, err := runnercontract.Parse(raw)
			require.NoError(t, err)
			factories := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories++
				if control == "failed" {
					return nil, errors.New("parent engine initialization control")
				}
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
			require.NoError(t, err)
			parentWire, err := json.Marshal(parentRequest)
			require.NoError(t, err)
			submission, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: parentWire,
			})
			require.NoError(t, err)
			original, err := service.Submit(t.Context(), submission, backend, hooks)
			require.NoError(t, err)
			require.NotNil(t, original.Completion)
			if control == "failed" || control == "canceled" {
				require.NotEqual(t, runnerterminal.Success, original.Terminal.Outcome)
			}
			parent := executionattempt.Source{ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
			parentRequest.Selection.Attempt++
			parentWire, err = json.Marshal(parentRequest)
			require.NoError(t, err)
			submission, err = json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: parentWire,
				Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &parent},
			})
			require.NoError(t, err)
			beforeReuse := factories
			reused, err := service.Submit(t.Context(), submission, backend, hooks)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, beforeReuse, factories)
			require.Equal(t, original.Terminal, reused.Terminal)
			require.Equal(t, original.Completion, reused.Completion)

			report := runnerunit.NewService(nil).Run(t.Context(), raw)
			require.Equal(t, "completed", report.Status)
			terminal, _ := report.Terminal()
			require.NotNil(t, terminal)
			require.Equal(t, runnerterminal.Success, terminal.Outcome)
			prepared, err := evidencestate.CaptureReport(report, nil)
			require.NoError(t, err)
			_, binding := report.Terminal()
			require.NotNil(t, binding)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			declaredParent := &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"}
			switch control {
			case "branch":
				declaredParent.Relationship = "branch"
			case "no-parent":
				declaredParent = nil
			}
			creator, err := ledger.Reserve(t.Context(), artifactmanifest.Binding{
				Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
			}, declaredParent)
			require.NoError(t, err)
			child, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
			require.NoError(t, err)
			require.NotNil(t, child.Document.ExecutionSource)
			childSource := *child.Document.ExecutionSource
			manifestBefore, err := backend.Get(t.Context(), "manifest-"+childSource.ManifestDigest, artifactmanifest.MaxManifestBytes)
			require.NoError(t, err)
			documentBefore, err := evidencestate.Encode(*child.Document)
			require.NoError(t, err)
			switch control {
			case "missing", "primary-missing":
				key := "completion-" + parent.ExecutionID
				if control == "primary-missing" {
					key = "reservation-" + childSource.ExecutionID
				}
				path := filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-"+key)
				require.FileExists(t, path)
				require.NoError(t, os.Remove(path))
			case "corrupt", "conflicting":
				path := filepath.Join(storePath, artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+parent.ExecutionID)
				changed := []byte("{}")
				if control == "conflicting" {
					record, err := ledger.ReadCompletion(t.Context(), parent.ExecutionID)
					require.NoError(t, err)
					completion := record.Completion
					completion.ManifestDigest = childSource.ManifestDigest
					changed, err = executionledger.EncodeCompletion(completion)
					require.NoError(t, err)
				}
				require.NoError(t, os.Chmod(path, 0o600))
				require.NoError(t, os.WriteFile(path, changed, 0o600))
			}
			pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: childSource.ManifestDigest, EvidenceSHA256: child.EvidenceSHA256}}
			if control == "verified" {
				reader, err := manifestaggregate.NewEvidenceReader(backend)
				require.NoError(t, err)
				inspection, categories, err := reader.InspectCategories(t.Context(), child.EvidenceSHA256)
				require.NoError(t, err)
				require.Len(t, categories, 6)
				qualified, err := reader.InspectSourceContext(t.Context(), *inspection.Document)
				require.NoError(t, err)
				require.Equal(t, parent, *qualified.ParentContext.Source)
				inspection.Document.Lineage = []evidencestate.Lineage{{Manifest: parent.ManifestDigest, Relationship: evidencestate.Retry}}
				withheld, err := reader.InspectSourceContext(t.Context(), *inspection.Document)
				require.ErrorIs(t, err, manifestaggregate.ErrUnavailable)
				require.Equal(t, "verified", withheld.State)
				require.Equal(t, "unavailable", withheld.ParentContext.State)
				require.Nil(t, withheld.ParentContext.Source, "another/mutated document cannot borrow the admitted category pass")
			}
			body, err := json.Marshal(manifestaggregate.Request{Manifests: []string{childSource.ManifestDigest}, Evidence: &pairs})
			require.NoError(t, err)
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutes(mux, directory, nil)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(body)))
			expectedState := "verified"
			expectedHTTP := http.StatusOK
			switch control {
			case "missing":
				expectedState, expectedHTTP = "unavailable", http.StatusConflict
			case "corrupt", "conflicting":
				expectedState, expectedHTTP = control, http.StatusConflict
			case "primary-missing":
				expectedHTTP = http.StatusConflict
			case "branch":
				expectedState = "unavailable"
			}
			require.Equal(t, expectedHTTP, response.Code, response.Body.String())
			var aggregate manifestaggregate.Report
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &aggregate))
			candidate := aggregate.Evidence.Members[0].Candidates[0]
			require.Equal(t, "verified", candidate.State)
			require.Len(t, candidate.Categories, 6)
			require.NotEmpty(t, aggregate.Evidence.CategoryStateCounts, "parent failure must not erase already admitted native facts")
			view := candidate.SourceContext
			require.NotNil(t, view)
			if control == "primary-missing" {
				require.Equal(t, "missing", view.State)
				require.Nil(t, view.ParentContext)
			} else {
				require.Equal(t, "verified", view.State, "parent failure cannot demote verified child context")
				if control == "no-parent" {
					require.Nil(t, view.ParentContext)
				} else {
					require.NotNil(t, view.ParentContext)
					require.Equal(t, expectedState, view.ParentContext.State)
					if expectedState == "verified" {
						require.Equal(t, parent, *view.ParentContext.Source)
					} else {
						require.Nil(t, view.ParentContext.Source)
					}
				}
			}
			csvBytes, err := manifestaggregate.EncodeCSV(aggregate)
			require.NoError(t, err)
			rows, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 2)
			require.Len(t, rows[0], 22)
			require.Equal(t, "source_context_json", rows[0][21])
			var csvView manifestaggregate.SourceContext
			require.NoError(t, json.Unmarshal([]byte(rows[1][21]), &csvView))
			require.Equal(t, *view, csvView)
			external := exec.CommandContext(t.Context(), binary, "aggregate", "--store", storePath,
				"--evidence", childSource.ManifestDigest+"="+child.EvidenceSHA256, childSource.ManifestDigest)
			external.Dir = directory
			wire, cliErr := external.Output()
			if expectedHTTP == http.StatusConflict {
				var exit *exec.ExitError
				require.ErrorAs(t, cliErr, &exit)
				require.Equal(t, 2, exit.ExitCode())
			} else {
				require.NoError(t, cliErr)
			}
			require.Equal(t, response.Body.String(), strings.TrimSuffix(string(wire), "\n"))
			node := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { sourceContextConsistent } from "../../web/src/lib/sourceContext.ts";
const report = JSON.parse(readFileSync(0, "utf8"));
const candidate = report.evidence.members[0].candidates[0];
const context = candidate.source_context;
assert.equal(sourceContextConsistent(context, candidate.origin_manifest, report.completion === "incomplete"), true);
const forged = { ...context, parent_context: {state:"verified",verification:"content-and-declared-context-only",relationship:"retry",source:context.source} };
assert.equal(sourceContextConsistent(forged, candidate.origin_manifest, true), false);
const nested = { ...forged, parent_context: {...forged.parent_context, parent_context:{state:"verified"}} };
assert.equal(sourceContextConsistent(nested, candidate.origin_manifest, true), false);
`)
			node.Stdin = bytes.NewReader(response.Body.Bytes())
			output, err := node.CombinedOutput()
			require.NoError(t, err, string(output))
			manifestAfter, err := backend.Get(t.Context(), "manifest-"+childSource.ManifestDigest, artifactmanifest.MaxManifestBytes)
			require.NoError(t, err)
			documentAfter, err := evidencestate.Encode(*child.Document)
			require.NoError(t, err)
			require.Equal(t, manifestBefore, manifestAfter)
			require.Equal(t, documentBefore, documentAfter)
			require.Empty(t, child.Document.Lineage)
		})
	}
}
