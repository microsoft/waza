package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/evidenceview"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestExecutionEvidenceParentContextActualPublicProducerAndDegradation(t *testing.T) {
	fixtures := map[string]struct {
		Digest      string `json:"digest"`
		Declaration string `json:"declaration"`
		Categories  string `json:"categories"`
		CSV         string `json:"csv"`
		Status      int    `json:"status"`
	}{}
	for _, control := range []string{"verified", "no-parent", "abandoned", "branch", "corrupt", "conflicting", "failed", "canceled"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			root := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(root, 0o700))
			wire := checkpointCLIWire(t)
			service := runnerunit.NewService(nil)
			if control == "failed" {
				wire = bytes.Replace(wire, []byte("- hello"), []byte("- absent-parent-context-control"), 1)
				require.Contains(t, string(wire), "absent-parent-context-control")
			}
			if control == "canceled" {
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) { return nil, context.Canceled })
			}
			original := runSubmissionWithService(t, service, root, wire)
			require.NotNil(t, original.Completion)
			parent := executionattempt.Source{ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
			backend, err := artifactmanifest.OpenLocal(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close()) }()
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			if control == "abandoned" {
				creator, err := ledger.Reserve(t.Context(), original.Reservation.Binding, nil)
				require.NoError(t, err)
				reservation, err := ledger.Reservation(creator)
				require.NoError(t, err)
				parent.ExecutionID = reservation.Attempt.ExecutionID
			}
			request, err := runnerunit.ParseSubmission(wire)
			require.NoError(t, err)
			request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct"}
			if control != "no-parent" {
				relationship := "retry"
				if control == "branch" {
					relationship = "branch"
				}
				request.Policy.Parent = &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: relationship}
			}
			wire, err = json.Marshal(request)
			require.NoError(t, err)
			child := runSubmissionCLI(t, root, wire)
			require.Equal(t, "completed", child.Status)
			require.Equal(t, map[bool]int{false: 0, true: 1}[control == "failed"], child.ExitCode)
			require.NotEqual(t, parent.ExecutionID, child.Reservation.ExecutionID)
			source := executionattempt.Source{ExecutionID: child.Reservation.ExecutionID, ManifestDigest: child.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName)
			require.NoError(t, err)
			index, err := evidencestate.ParseCapture(completed.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
			require.NoError(t, err)
			document := evidencestate.Document{Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version,
				Publication: "derived", OriginManifest: source.ManifestDigest, ExecutionSource: &source,
				AttemptArtifact: "execution-attempt.json", Categories: index.Categories, Lineage: []evidencestate.Lineage{}}
			published, err := evidencestate.Publish(t.Context(), backend, document)
			require.NoError(t, err)
			_, nativeBefore, err := evidencestate.InspectCategories(t.Context(), backend, published.EvidenceSHA256)
			require.NoError(t, err)
			before, err := evidencestate.Encode(document)
			require.NoError(t, err)
			if control == "corrupt" || control == "conflicting" {
				parentCompleted, err := ledger.ReadCompleted(t.Context(), parent, "execution-attempt.json")
				require.NoError(t, err)
				path := filepath.Join(root, "blob-"+artifactmanifest.Hash(parentCompleted.Reservation.Bytes))
				data := bytes.Clone(parentCompleted.Reservation.Bytes)
				if control == "conflicting" {
					changed := parentCompleted.Reservation.Attempt
					changed.Binding.Selection.Attempt++
					data, err = executionattempt.EncodeAttempt(changed)
					require.NoError(t, err)
					path = filepath.Join(root, artifactmanifest.ExecutionLedgerNamespaceV1+"-reservation-"+parent.ExecutionID)
				} else {
					data[0] ^= 1
				}
				require.NoError(t, os.Chmod(path, 0o600))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			output, cliErr := runArtifactCLI(t, directory, "evidence", "categories", published.EvidenceSHA256)
			degraded := control == "abandoned" || control == "corrupt" || control == "conflicting"
			status := http.StatusOK
			if degraded {
				status = http.StatusConflict
				exit, ok := errors.AsType[*ExitCodeError](cliErr)
				require.True(t, ok, "%v", cliErr)
				require.Equal(t, 2, exit.Code)
			} else {
				require.NoError(t, cliErr)
			}
			mux := http.NewServeMux()
			webapi.RegisterEvidenceRoutes(mux, directory, nil)
			csvBytes := ""
			for _, suffix := range []string{"", "/document", "/csv"} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+published.EvidenceSHA256+"/categories"+suffix, nil))
				require.Equal(t, status, response.Code, "%s", response.Body.Bytes())
				if degraded || suffix == "" || suffix == "/document" {
					require.JSONEq(t, string(output), response.Body.String())
				}
				if degraded {
					require.Empty(t, response.Header().Get("Content-Disposition"))
				}
				if suffix == "/csv" {
					csvBytes = response.Body.String()
				}
			}
			var view evidenceview.Categories
			require.NoError(t, json.Unmarshal(output, &view))
			require.Equal(t, "verified", view.Integrity)
			require.Len(t, view.Categories, 6)
			require.Equal(t, nativeBefore, view.Categories, "optional parent failures must not rewrite independently admitted child facts")
			require.Empty(t, view.Document.Lineage)
			if control == "no-parent" {
				require.Nil(t, view.SourceContext)
			} else {
				require.NotNil(t, view.SourceContext)
				require.Equal(t, "verified", view.SourceContext.State)
				require.NotNil(t, view.SourceContext.ParentContext)
				switch control {
				case "abandoned", "branch":
					require.Equal(t, "unavailable", view.SourceContext.ParentContext.State)
				case "corrupt":
					require.Equal(t, "corrupt", view.SourceContext.ParentContext.State)
				case "conflicting":
					require.Equal(t, "conflicting", view.SourceContext.ParentContext.State)
				}
				if degraded || control == "branch" {
					require.Nil(t, view.SourceContext.ParentContext.Source)
				} else {
					require.Equal(t, "verified", view.SourceContext.ParentContext.State)
					require.Equal(t, &parent, view.SourceContext.ParentContext.Source)
				}
			}
			node := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", `
import assert from "node:assert/strict"; import {readFileSync} from "node:fs";
import {parseRecordedCategories} from "../../web/src/api/executionEvidenceCategories.ts";
const wire=readFileSync(0,"utf8"), original=JSON.parse(wire);
const view=parseRecordedCategories(wire, original.evidence_sha256, Number(process.argv[1]));
assert.equal(view.categories.length,6); assert.equal(view.sourceVerified,true);
assert.equal(view.admissionComplete,process.argv[1]==="200");
assert.equal(original.document.lineage.length,0);
`, strconv.Itoa(status))
			node.Stdin = bytes.NewReader(output)
			nodeOutput, err := node.CombinedOutput()
			require.NoError(t, err, "%s", nodeOutput)
			after, err := backend.Get(t.Context(), "blob-"+published.EvidenceSHA256, evidencestate.MaxDocumentBytes)
			require.NoError(t, err)
			require.Equal(t, before, after)
			manifest, err := backend.Get(t.Context(), "manifest-"+source.ManifestDigest, artifactmanifest.MaxManifestBytes)
			require.NoError(t, err)
			require.Equal(t, completed.Publication.ManifestBytes, manifest)
			declaration, err := evidencestate.Inspect(t.Context(), backend, published.EvidenceSHA256)
			require.NoError(t, err)
			declarationBytes, err := json.Marshal(declaration)
			require.NoError(t, err)
			fixtures[control] = struct {
				Digest      string `json:"digest"`
				Declaration string `json:"declaration"`
				Categories  string `json:"categories"`
				CSV         string `json:"csv"`
				Status      int    `json:"status"`
			}{published.EvidenceSHA256, string(declarationBytes), string(output), csvBytes, status}
		})
	}
	if output := os.Getenv("WAZA_PARENT_CATEGORY_FIXTURES"); output != "" && !t.Failed() {
		require.Len(t, fixtures, 8)
		data, err := json.Marshal(fixtures)
		require.NoError(t, err)
		file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, writeErr := file.Write(data)
		require.NoError(t, errors.Join(writeErr, file.Close()))
	}
}
