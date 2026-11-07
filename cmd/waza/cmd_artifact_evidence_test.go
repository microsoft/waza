package main

import (
	"bytes"
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

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestArtifactEvidenceCLIToPersistedAPIAndDownloads(t *testing.T) {
	directory, manifestPath, origin, _ := artifactCLIFixture(t)
	digest, err := artifactmanifest.Digest(origin)
	require.NoError(t, err)
	_, err = runArtifactCLI(t, directory, "create", manifestPath)
	require.NoError(t, err)
	for _, name := range []string{"terminal", "empty.txt"} {
		path := filepath.Join(directory, name)
		if name == "terminal" {
			path += ".json"
		}
		_, err = runArtifactCLI(t, directory, "upload", manifestPath, name, path)
		require.NoError(t, err)
	}
	_, err = runArtifactCLI(t, directory, "finalize", digest)
	require.NoError(t, err)
	zero, precise := int64(0), int64(9007199254740993)
	d := evidencestate.Document{
		Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
		OriginManifest: digest, Lineage: []evidencestate.Lineage{},
		Categories: []evidencestate.Record{
			{Category: evidencestate.Conversation, State: evidencestate.Present, Source: evidencestate.Authored, Context: evidencestate.Declared, Artifacts: []string{}, ObservedCount: &zero},
			{Category: evidencestate.Tool, State: evidencestate.Unavailable, Source: evidencestate.Unknown, Context: evidencestate.UnknownContext, Artifacts: []string{}, Reason: evidencestate.NotRecorded},
			{Category: evidencestate.Grader, State: evidencestate.Redacted, Source: evidencestate.Mock, Context: evidencestate.Observed, Artifacts: []string{}, Reason: evidencestate.Privacy},
			{Category: evidencestate.Reliability, State: evidencestate.Conflicting, Source: evidencestate.Authored, Context: evidencestate.Declared, Artifacts: []string{}, Reason: evidencestate.SourceConflict},
			{Category: evidencestate.Usage, State: evidencestate.Incomplete, Source: evidencestate.Mock, Context: evidencestate.UnknownContext, Artifacts: []string{}, Reason: evidencestate.PartialCapture},
			{Category: evidencestate.Provenance, State: evidencestate.Incomplete, Source: evidencestate.Authored, Context: evidencestate.Declared, Artifacts: []string{}, ObservedCount: &precise, Reason: evidencestate.ArtifactCountLimit, Limit: &evidencestate.Limit{Observed: 129}},
		},
	}
	data, err := evidencestate.Encode(d)
	require.NoError(t, err)
	evidencePath := filepath.Join(directory, "evidence.json")
	require.NoError(t, os.WriteFile(evidencePath, data, 0o600))
	output, err := runArtifactCLI(t, directory, "evidence", "publish", evidencePath)
	require.NoError(t, err)
	var receipt evidencestate.Inspection
	require.NoError(t, json.Unmarshal(output, &receipt))
	require.Equal(t, "derived", receipt.Publication)
	require.Equal(t, "verified", receipt.Integrity)
	require.Equal(t, artifactmanifest.Hash(data), receipt.EvidenceSHA256)
	require.NotContains(t, string(output), `"passed"`)
	require.NotContains(t, string(output), `"ai_credits"`)
	categoryOutput, err := runArtifactCLI(t, directory, "evidence", "categories", receipt.EvidenceSHA256)
	require.NoError(t, err)
	var categoryReceipt struct {
		evidencestate.Inspection
		Categories []evidencestate.CategoryEvidence `json:"categories"`
	}
	require.NoError(t, json.Unmarshal(categoryOutput, &categoryReceipt))
	require.Len(t, categoryReceipt.Categories, 6)
	categoryFixture, err := os.ReadFile("../../web/e2e/fixtures/execution-evidence-category-results.json")
	require.NoError(t, err)
	categoryBytes, err := json.Marshal(categoryReceipt.Categories)
	require.NoError(t, err)
	require.JSONEq(t, string(categoryFixture), string(categoryBytes), "browser admission fixture must come from the actual CLI source pass")
	for _, category := range categoryReceipt.Categories {
		require.Equal(t, evidencestate.Unavailable, category.State, "legacy declarations without the actual capture index cannot qualify a category")
		require.Equal(t, "declared-state-only", category.Verification)
		require.Nil(t, category.ObservedCount)
		require.Nil(t, category.Usage)
	}
	mux := http.NewServeMux()
	webapi.RegisterEvidenceRoutes(mux, directory, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	get := func(path string) (int, http.Header, []byte) {
		t.Helper()
		response, err := http.Get(server.URL + "/api/v1/evidence/" + receipt.EvidenceSHA256 + path)
		require.NoError(t, err)
		content, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response.StatusCode, response.Header, content
	}
	status, _, inspected := get("")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(inspected), `"observed_count":9007199254740993`)
	fixture, err := os.ReadFile("../../web/e2e/fixtures/execution-evidence.json")
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(fixture)), strings.TrimSpace(string(inspected)), "browser fixture must be emitted by the actual persisted CLI-to-API path")
	status, _, categoryAPI := get("/categories")
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, string(categoryOutput), string(categoryAPI), "CLI and API must share actual one-pass category admission")
	status, headers, categoryDocument := get("/categories/document")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "application/json", headers.Get("Content-Type"))
	require.Equal(t, `attachment; filename="execution-evidence-categories-`+categoryReceipt.EvidenceSHA256+`.json"`, headers.Get("Content-Disposition"))
	require.Equal(t, strings.TrimSpace(string(categoryOutput)), string(categoryDocument))
	qualifiedJSON := strings.TrimSpace(string(fixture))
	qualifiedJSON = qualifiedJSON[:len(qualifiedJSON)-1] + `,"categories":` + strings.TrimSpace(string(categoryFixture)) + "}"
	require.Equal(t, qualifiedJSON, string(categoryDocument), "qualified browser JSON must preserve exact native producer bytes")
	status, headers, categoryCSV := get("/categories/csv")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "text/csv; charset=utf-8", headers.Get("Content-Type"))
	categoryRows, err := csv.NewReader(bytes.NewReader(categoryCSV)).ReadAll()
	require.NoError(t, err)
	require.Len(t, categoryRows, 7)
	require.Equal(t, []string{"category", "declared_state", "admitted_state", "verification", "reason", "observed_count",
		"turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "premium_requests", "ai_credits",
		"declared_source", "declared_context", "origin_manifest", "evidence_sha256", "execution_id"}, categoryRows[0])
	for _, row := range categoryRows[1:] {
		require.Equal(t, "unavailable", row[2])
		for _, absent := range row[5:13] {
			require.Empty(t, absent, "unqualified counts and usage must never export declaration values")
		}
		require.Equal(t, receipt.Document.OriginManifest, row[15])
		require.Equal(t, receipt.EvidenceSHA256, row[16])
	}
	require.NotContains(t, string(categoryCSV), "9007199254740993")
	qualifiedFixture, err := os.ReadFile("../../web/e2e/fixtures/execution-evidence-qualified.csv")
	require.NoError(t, err)
	require.Equal(t, string(qualifiedFixture), string(categoryCSV), "qualified browser download must match the actual production API")
	status, headers, downloaded := get("/document")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "application/json", headers.Get("Content-Type"))
	require.Contains(t, headers.Get("Content-Disposition"), ".json")
	require.Equal(t, data, downloaded, "download must retain canonical numeric precision and presence")
	status, headers, downloaded = get("/csv")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, headers.Get("Content-Type"), "text/csv")
	csvFixture, err := os.ReadFile("../../web/e2e/fixtures/execution-evidence.csv")
	require.NoError(t, err)
	require.Equal(t, string(csvFixture), string(downloaded), "browser CSV fixture must be emitted by the actual persisted API")
	rows, err := csv.NewReader(strings.NewReader(string(downloaded))).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 7, "all six categories, not a vacuous download")
	found := map[string][]string{}
	for _, row := range rows[1:] {
		found[row[0]] = row
	}

	require.Equal(t, "0", found["conversation"][4], "explicit recorded zero is not absence")
	require.Equal(t, "", found["tool"][4], "unavailable count is not fabricated zero")
	require.Equal(t, "9007199254740993", found["provenance"][4])
	require.Equal(t, "", found["provenance"][10], "unknown total is not fabricated")
	require.Equal(t, "", found["grader"][6], "redacted references remain absent")
	require.Equal(t, "conflicting", found["reliability"][1])
	require.NoError(t, os.Remove(filepath.Join(directory, ".waza-artifacts", "final-"+digest)))
	status, _, withheld := get("/document")
	require.Equal(t, http.StatusNotFound, status)
	require.NotContains(t, string(withheld), `"document"`, "stale successful evidence must not be downloaded")
	output, err = runArtifactCLI(t, directory, "evidence", "inspect", receipt.EvidenceSHA256)
	require.Error(t, err)
	require.NotContains(t, string(output), `"document"`)
	receipt = evidencestate.Inspection{}
	require.NoError(t, json.Unmarshal(output, &receipt))
	require.Equal(t, "failed", receipt.Integrity)
	require.Nil(t, receipt.Document)
	categoryOutput, err = runArtifactCLI(t, directory, "evidence", "categories", categoryReceipt.EvidenceSHA256)
	require.Error(t, err)
	categoryReceipt.Categories = nil
	categoryReceipt.Inspection = evidencestate.Inspection{}
	require.NoError(t, json.Unmarshal(categoryOutput, &categoryReceipt))
	require.Len(t, categoryReceipt.Categories, 6)
	for _, category := range categoryReceipt.Categories {
		require.Equal(t, evidencestate.Unavailable, category.State)
		require.Empty(t, category.DeclaredState)
		require.Nil(t, category.ObservedCount)
		require.Nil(t, category.Usage)
	}
	status, _, categoryAPI = get("/categories")
	require.Equal(t, http.StatusNotFound, status)
	require.JSONEq(t, string(categoryOutput), string(categoryAPI))
	parser := exec.Command("node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseRecordedCategories } from "../../web/src/api/executionEvidenceCategories.ts";
const wire = readFileSync(0, "utf8");
const result = parseRecordedCategories(wire, JSON.parse(wire).evidence_sha256, 404);
assert.equal(result.sourceVerified, false);
assert.equal(result.admissionComplete, false);
assert.equal(result.categories.length, 6);
for (const category of result.categories) {
  assert.equal(category.state, "unavailable");
  assert.equal(category.observed_count, undefined);
  assert.equal(category.usage, undefined);
}
`)
	parser.Stdin = bytes.NewReader(categoryAPI)
	parserOutput, err := parser.CombinedOutput()
	require.NoError(t, err, string(parserOutput))
	for _, suffix := range []string{"/categories/document", "/categories/csv"} {
		status, headers, body := get(suffix)
		require.Equal(t, http.StatusNotFound, status)
		require.Empty(t, headers.Get("Content-Disposition"), "failed exports must not return success-shaped attachments")
		require.NotContains(t, string(body), "9007199254740993")
	}
}

func TestArtifactEvidenceInvalidInputDoesNotOpenStore(t *testing.T) {
	directory := t.TempDir()
	output, err := runArtifactCLI(t, directory, "evidence", "inspect", "invalid")
	require.ErrorIs(t, err, evidencestate.ErrInvalid)
	require.Empty(t, output)
	_, err = os.Stat(filepath.Join(directory, ".waza-artifacts"))
	require.ErrorIs(t, err, os.ErrNotExist)
	mux := http.NewServeMux()
	webapi.RegisterEvidenceRoutes(mux, directory, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/invalid", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NotContains(t, w.Body.String(), directory, "private paths must not leak through API errors")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/invalid/categories", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NotContains(t, w.Body.String(), directory)
	require.NotContains(t, w.Body.String(), `"usage":`)
}

func TestArtifactEvidenceCategoriesReadsActualOwnedCapture(t *testing.T) {
	wire, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	report := runnerunit.NewService(nil).Run(t.Context(), wire)
	require.Equal(t, 1, report.Calls)
	require.NotNil(t, report.Observation)
	prepared, err := evidencestate.CaptureReport(report, nil)
	require.NoError(t, err)
	directory := t.TempDir()
	storeDirectory := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storeDirectory, 0o700))
	backend, err := artifactmanifest.OpenLocal(storeDirectory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	terminal, binding := report.Terminal()
	require.NotNil(t, binding)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	creator, err := ledger.Reserve(t.Context(), artifactmanifest.Binding{
		Identity: *binding.Identity, Selection: binding.Selection,
		ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
	}, nil)
	require.NoError(t, err)
	published, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
	require.NoError(t, err)
	output, err := runArtifactCLI(t, directory, "evidence", "categories", published.EvidenceSHA256)
	require.NoError(t, err)
	var response struct {
		Categories []evidencestate.CategoryEvidence `json:"categories"`
	}
	require.NoError(t, json.Unmarshal(output, &response))
	require.Len(t, response.Categories, 6)
	found := map[evidencestate.Category]evidencestate.CategoryEvidence{}
	for _, category := range response.Categories {
		found[category.Category] = category
	}
	require.Equal(t, evidencestate.Redacted, found[evidencestate.Conversation].State)
	require.Equal(t, evidencestate.Redacted, found[evidencestate.Tool].State)
	require.Equal(t, evidencestate.Unavailable, found[evidencestate.Usage].State)
	require.Nil(t, found[evidencestate.Usage].Usage)
	require.Equal(t, evidencestate.Present, found[evidencestate.Reliability].State)
	require.Equal(t, int64(1), *found[evidencestate.Reliability].ObservedCount)
	require.Equal(t, evidencestate.Present, found[evidencestate.Grader].State)
	require.Contains(t, found[evidencestate.Grader].Verification, "recorded-category-shape")
	require.Equal(t, 1, terminal.EngineCalls, "inspection must not execute the report again")
	require.NotContains(t, string(output), `"tool_events"`)
	require.NotContains(t, string(output), `"transcript"`)
	require.NotContains(t, string(output), `"final_output"`)
	mux := http.NewServeMux()
	webapi.RegisterEvidenceRoutesWithBackend(mux, backend)
	readAPI := func(digest string) (int, []byte) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+digest+"/categories", nil))
		return w.Code, w.Body.Bytes()
	}
	status, apiOutput := readAPI(published.EvidenceSHA256)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, string(output), string(apiOutput))

	// A separately authored, byte-valid declaration falsely claims empty
	// grader evidence. It must not erase independently verified reliability.
	store := artifactmanifest.NewStore(backend)
	source, err := store.ReadCompleted(t.Context(), published.Document.OriginManifest, evidencestate.CaptureArtifactName)
	require.NoError(t, err)
	index, err := evidencestate.ParseCapture(source.ArtifactBytes[evidencestate.CaptureArtifactName])
	require.NoError(t, err)
	for i := range index.Categories {
		if index.Categories[i].Category == evidencestate.Grader {
			zero := int64(0)
			index.Categories[i].State = evidencestate.Present
			index.Categories[i].ObservedCount = &zero
			index.Categories[i].Artifacts = []string{}
		}
	}
	index.EmptyPayloads[evidencestate.Grader] = json.RawMessage(`{}`)
	indexBytes, err := evidencestate.EncodeCapture(*index)
	require.NoError(t, err)
	manifest := source.Manifest
	for i, artifact := range manifest.Artifacts {
		if artifact.Name == evidencestate.CaptureArtifactName {
			artifact.SHA256, artifact.SizeBytes = artifactmanifest.Hash(indexBytes), int64(len(indexBytes))
			manifest.Artifacts[i] = artifact
			require.NoError(t, store.Put(t.Context(), artifact, indexBytes))
		}
	}
	manifestBytes, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	origin, err := store.Create(t.Context(), manifestBytes)
	require.NoError(t, err)
	_, err = store.Finalize(t.Context(), origin)
	require.NoError(t, err)
	documentBytes, err := evidencestate.Encode(*published.Document)
	require.NoError(t, err)
	document, err := evidencestate.Parse(documentBytes)
	require.NoError(t, err)
	document.OriginManifest, document.ExecutionSource, document.Categories = origin, nil, index.Categories
	document.AttemptArtifact = ""
	conflicting, err := evidencestate.Publish(t.Context(), backend, *document)
	require.NoError(t, err, "byte/context publication is separate from category semantics")
	output, err = runArtifactCLI(t, directory, "evidence", "categories", conflicting.EvidenceSHA256)
	require.ErrorIs(t, err, artifactmanifest.ErrConflict)
	response.Categories = nil
	require.NoError(t, json.Unmarshal(output, &response))
	require.Contains(t, string(output), `"category_admission_failed"`)
	for _, category := range response.Categories {
		switch category.Category {
		case evidencestate.Grader:
			require.Equal(t, evidencestate.Conflicting, category.State)
			require.Nil(t, category.ObservedCount)
		case evidencestate.Reliability:
			require.Equal(t, evidencestate.Present, category.State)
			require.Equal(t, int64(1), *category.ObservedCount)
		}
	}
	status, apiOutput = readAPI(conflicting.EvidenceSHA256)
	require.Equal(t, http.StatusConflict, status)
	require.JSONEq(t, string(output), string(apiOutput), "independent verified facts survive semantic conflict in both public readers")
}

func TestExecutionEvidenceIntegerTokensPreserveWirePrecision(t *testing.T) {
	output, err := exec.Command("node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { parseStrictJSON } from "../../web/src/lib/strictJson.ts";
const wire = '{"n":9007199254740993,"zero":0,"fraction":1.5,"text":"\\\"n\\\":9007199254740993","nested":[9223372036854775807]}';
const preserved = parseStrictJSON(wire, true);
assert.equal(preserved.n, "9007199254740993");
assert.equal(preserved.zero, "0");
assert.equal(preserved.fraction, 1.5);
assert.equal(preserved.text, '"n":9007199254740993');
assert.equal(preserved.nested[0], "9223372036854775807");
assert.equal(parseStrictJSON('{"n":7}').n, 7);
assert.throws(() => parseStrictJSON('{"n":1,"\\u006e":2}', true));
assert.throws(() => parseStrictJSON('{"n":"\\ud800"}', true));
`).CombinedOutput()
	require.NoError(t, err, string(output))
}
