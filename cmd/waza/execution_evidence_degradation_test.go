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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

type categoryDegradationFixture struct {
	directory string
	storeRoot string
	backend   artifactmanifest.Backend
	receipt   evidencestate.Inspection
	manifest  []byte
}

func categoryDegradationReport(t *testing.T) runnerunit.Report {
	t.Helper()
	wire, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	report := runnerunit.NewService(nil).Run(t.Context(), wire)
	require.Equal(t, 1, report.Calls)
	require.NotNil(t, report.Observation)
	// These authored controls on a real mock report are not original SDK
	// observations, live billing, or authority to publish private content.
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"user.message","content":"category-private-conversation"}]`), &report.Observation.Transcript))
	report.Observation.ToolEvents = []models.ToolEvent{{
		Turn: 1, Sequence: 1, ToolName: "control", Success: true, Result: "category-private-tool",
	}}
	require.NotEmpty(t, report.Observation.Validations)
	for name, result := range report.Observation.Validations {
		result.Feedback = "category-private-grader"
		report.Observation.Validations[name] = result
	}
	zero := 0.0
	report.Observation.Usage = &models.UsageStats{
		AICredits: &zero, ProviderHost: "category-private-usage.example",
		ModelMetrics: map[string]models.ModelUsage{"mock-control": {AICredits: &zero}},
	}
	report.Status, report.Observation.Status = "execution_error", models.StatusError
	report.Observation.ErrorMsg = "category-private-reliability"
	return report
}

func newCategoryDegradationFixture(t *testing.T, withheld []evidencestate.Category) categoryDegradationFixture {
	t.Helper()
	report := categoryDegradationReport(t)
	prepared, err := evidencestate.CaptureReport(report, withheld)
	require.NoError(t, err)
	directory := t.TempDir()
	storeRoot := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storeRoot, 0o700))
	backend, err := artifactmanifest.OpenLocal(storeRoot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	_, binding := report.Terminal()
	require.NotNil(t, binding)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	creator, err := ledger.Reserve(t.Context(), artifactmanifest.Binding{
		Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
	}, nil)
	require.NoError(t, err)
	receipt, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
	require.NoError(t, err)
	require.NotNil(t, receipt.Document)
	source, err := artifactmanifest.NewStore(backend).ReadCompleted(t.Context(), receipt.Document.OriginManifest)
	require.NoError(t, err)
	return categoryDegradationFixture{directory, storeRoot, backend, receipt, bytes.Clone(source.ManifestBytes)}
}

func (f categoryDegradationFixture) get(t *testing.T, digest, suffix string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	webapi.RegisterEvidenceRoutesWithBackend(mux, f.backend)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+digest+"/categories"+suffix, nil))
	return response
}

func checkDegradedCategoryParser(t *testing.T, response *httptest.ResponseRecorder, sourceVerified bool, redactedCategory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	node := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseRecordedCategories } from "../../web/src/api/executionEvidenceCategories.ts";
const wire = readFileSync(0, "utf8");
const result = parseRecordedCategories(wire, JSON.parse(wire).evidence_sha256, Number(process.argv[1]));
assert.equal(result.sourceVerified, process.argv[2] === "true");
assert.equal(result.admissionComplete, Number(process.argv[1]) === 200);
assert.equal(result.categories.length, 6);
for (const category of result.categories) {
  if (process.argv[3] === "" || category.category === process.argv[3]) {
    assert.equal(category.state, process.argv[3] === "" ? "unavailable" : "redacted");
    assert.equal(category.observed_count, undefined);
    assert.equal(category.usage, undefined);
  } else {
    assert.equal(category.state, "present");
    assert.ok(BigInt(category.observed_count) > 0n);
  }
}
`, strconv.Itoa(response.Code), strconv.FormatBool(sourceVerified), redactedCategory)
	node.Stdin = bytes.NewReader(response.Body.Bytes())
	output, err := node.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestExecutionEvidenceEveryCategoryRemovalCorruptionAndDeclarationConflict(t *testing.T) {
	for _, category := range []evidencestate.Category{
		evidencestate.Conversation, evidencestate.Tool, evidencestate.Grader,
		evidencestate.Reliability, evidencestate.Usage, evidencestate.Provenance,
	} {
		for _, fault := range []string{"removed", "corrupt", "conflicting-declaration"} {
			t.Run(string(category)+"/"+fault, func(t *testing.T) {
				fixture := newCategoryDegradationFixture(t, []evidencestate.Category{})
				baseline := fixture.get(t, fixture.receipt.EvidenceSHA256, "")
				require.Equal(t, http.StatusOK, baseline.Code)
				var original struct {
					Categories []evidencestate.CategoryEvidence `json:"categories"`
				}
				require.NoError(t, json.Unmarshal(baseline.Body.Bytes(), &original))
				require.Len(t, original.Categories, 6)
				for _, recorded := range original.Categories {
					require.Equal(t, evidencestate.Present, recorded.State)
					require.NotNil(t, recorded.ObservedCount)
					require.Positive(t, *recorded.ObservedCount)
				}
				digest := fixture.receipt.EvidenceSHA256
				status, expectedError := http.StatusConflict, artifactmanifest.ErrCorrupt
				sourceVerified := false
				if fault == "conflicting-declaration" {
					wire, err := evidencestate.Encode(*fixture.receipt.Document)
					require.NoError(t, err)
					document, err := evidencestate.Parse(wire)
					require.NoError(t, err)
					changed := false
					for i := range document.Categories {
						record := &document.Categories[i]
						if record.Category == category {
							require.NotNil(t, record.ObservedCount)
							count := *record.ObservedCount + 1
							record.ObservedCount, changed = &count, true
						}
					}
					require.True(t, changed)
					published, err := evidencestate.Publish(t.Context(), fixture.backend, *document)
					require.NoError(t, err, "authored byte-valid declarations do not establish category semantics")
					digest, expectedError, sourceVerified = published.EvidenceSHA256, artifactmanifest.ErrConflict, true
					require.NotEqual(t, fixture.receipt.EvidenceSHA256, digest)
				} else {
					source, err := artifactmanifest.NewStore(fixture.backend).ReadCompleted(t.Context(), fixture.receipt.Document.OriginManifest)
					require.NoError(t, err)
					name := "execution-evidence-" + string(category) + ".json"
					switch category {
					case evidencestate.Reliability:
						name = "terminal.json"
					case evidencestate.Provenance:
						name = evidencestate.CaptureArtifactName
					}
					var artifact *artifactmanifest.Artifact
					for i := range source.Manifest.Artifacts {
						if source.Manifest.Artifacts[i].Name == name {
							artifact = &source.Manifest.Artifacts[i]
						}
					}
					require.NotNil(t, artifact, "the selected category must have an actual source artifact")
					path := filepath.Join(fixture.storeRoot, "blob-"+artifact.SHA256)
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					require.NotEmpty(t, data)
					if fault == "removed" {
						require.NoError(t, os.Remove(path))
						status, expectedError = http.StatusNotFound, artifactmanifest.ErrMissing
					} else {
						data[0] ^= 1
						require.NotEqual(t, artifact.SHA256, artifactmanifest.Hash(data))
						require.Equal(t, artifact.SizeBytes, int64(len(data)), "same-size corruption must still fail integrity")
						require.NoError(t, os.Chmod(path, 0o600))
						require.NoError(t, os.WriteFile(path, data, 0o600))
					}
				}
				output, err := runArtifactCLI(t, fixture.directory, "evidence", "categories", digest)
				require.ErrorIs(t, err, expectedError)
				response := fixture.get(t, digest, "")
				require.Equal(t, status, response.Code)
				require.JSONEq(t, string(output), response.Body.String())
				checkDegradedCategoryParser(t, response, sourceVerified, "")
				for _, suffix := range []string{"/document", "/csv"} {
					export := fixture.get(t, digest, suffix)
					require.Equal(t, status, export.Code)
					require.Empty(t, export.Header().Get("Content-Disposition"))
					require.JSONEq(t, response.Body.String(), export.Body.String())
				}
				manifest, err := fixture.backend.Get(t.Context(), "manifest-"+fixture.receipt.Document.OriginManifest, artifactmanifest.MaxManifestBytes)
				require.NoError(t, err)
				require.Equal(t, fixture.manifest, manifest, "degradation and derived claims must never rewrite the origin")
			})
		}
	}
}

func TestExecutionEvidenceEveryWithholdableCategoryIsAbsentFromPersistenceAndExports(t *testing.T) {
	for _, category := range []evidencestate.Category{
		evidencestate.Conversation, evidencestate.Tool, evidencestate.Grader, evidencestate.Reliability, evidencestate.Usage,
	} {
		t.Run(string(category), func(t *testing.T) {
			fixture := newCategoryDegradationFixture(t, []evidencestate.Category{category})
			source, err := artifactmanifest.NewStore(fixture.backend).ReadCompleted(t.Context(), fixture.receipt.Document.OriginManifest)
			require.NoError(t, err)
			marker := "category-private-" + string(category)
			for _, artifact := range source.Manifest.Artifacts {
				data, err := fixture.backend.Get(t.Context(), "blob-"+artifact.SHA256, artifactmanifest.MaxArtifactBytes)
				require.NoError(t, err)
				require.NotContains(t, string(data), marker, "withholding must happen before persistence")
			}
			output, err := runArtifactCLI(t, fixture.directory, "evidence", "categories", fixture.receipt.EvidenceSHA256)
			require.NoError(t, err)
			response := fixture.get(t, fixture.receipt.EvidenceSHA256, "")
			require.Equal(t, http.StatusOK, response.Code)
			require.JSONEq(t, string(output), response.Body.String())
			checkDegradedCategoryParser(t, response, true, string(category))
			var receipt struct {
				Categories []evidencestate.CategoryEvidence `json:"categories"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &receipt))
			require.Len(t, receipt.Categories, 6)
			found := false
			for _, recorded := range receipt.Categories {
				if recorded.Category == category {
					found = true
					require.Equal(t, evidencestate.Redacted, recorded.State)
					require.Nil(t, recorded.ObservedCount)
					require.Nil(t, recorded.Usage)
				}
			}
			require.True(t, found)
			document := fixture.get(t, fixture.receipt.EvidenceSHA256, "/document")
			require.Equal(t, http.StatusOK, document.Code)
			require.NotEmpty(t, document.Header().Get("Content-Disposition"))
			require.JSONEq(t, response.Body.String(), document.Body.String())
			export := fixture.get(t, fixture.receipt.EvidenceSHA256, "/csv")
			require.Equal(t, http.StatusOK, export.Code)
			require.NotEmpty(t, export.Header().Get("Content-Disposition"))
			rows, err := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 7)
			found = false
			for _, row := range rows[1:] {
				if row[0] == string(category) {
					found = true
					require.Equal(t, "redacted", row[2])
					require.Empty(t, row[5])
					for _, value := range row[6:13] {
						require.Empty(t, value)
					}
				}
			}
			require.True(t, found)
			require.NotContains(t, string(output)+document.Body.String()+export.Body.String(), marker)
		})
	}
	t.Run("required provenance cannot be privacy-waived", func(t *testing.T) {
		prepared, err := evidencestate.CaptureReport(categoryDegradationReport(t), []evidencestate.Category{evidencestate.Provenance})
		require.ErrorIs(t, err, evidencestate.ErrInvalid)
		require.Nil(t, prepared, "a missing required binding cannot produce a capture publication")
	})
}
