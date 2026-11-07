package main

import (
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
