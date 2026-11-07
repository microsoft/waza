package main

import (
	"bytes"
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

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestExecutionEvidenceCategoryUIAndExportsUseActualNativePresenceAndPrecision(t *testing.T) {
	for _, control := range []string{"missing-credits", "zero-credits", "fractional-credits"} {
		t.Run(control, func(t *testing.T) {
			wire, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			report := runnerunit.NewService(nil).Run(t.Context(), wire)
			require.NotNil(t, report.Observation)
			// Authored presence/usage controls on a real mock report are not
			// claims of independently original SDK usage or live billing.
			report.Observation.Transcript = []models.TranscriptEvent{}
			report.Observation.ToolEvents = []models.ToolEvent{}
			var credits *float64
			if control != "missing-credits" {
				value := 0.0
				if control == "fractional-credits" {
					value = 0.12345678901234568
				}
				credits = &value
			}
			report.Observation.Usage = &models.UsageStats{
				InputTokens: 9007199254740993, AICredits: credits,
				ModelMetrics: map[string]models.ModelUsage{"mock-control": {AICredits: credits}},
			}
			prepared, err := evidencestate.CaptureReport(report, []evidencestate.Category{})
			require.NoError(t, err)
			directory := t.TempDir()
			storeDirectory := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storeDirectory, 0o700))
			backend, err := artifactmanifest.OpenLocal(storeDirectory)
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
			published, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
			require.NoError(t, err)
			mux := http.NewServeMux()
			webapi.RegisterEvidenceRoutesWithBackend(mux, backend)
			get := func(suffix string) *httptest.ResponseRecorder {
				t.Helper()
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+published.EvidenceSHA256+"/categories"+suffix, nil))
				require.Equal(t, http.StatusOK, response.Code)
				return response
			}
			response := get("")
			wantCredits := ""
			if credits != nil {
				wantCredits = strconv.FormatFloat(*credits, 'g', -1, 64)
			}
			node := exec.Command("node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseRecordedCategories } from "../../web/src/api/executionEvidenceCategories.ts";
const text = readFileSync(0, "utf8");
const original = JSON.parse(text);
const result = parseRecordedCategories(text, original.evidence_sha256, 200);
assert.equal(result.categories.length, 6);
assert.equal(result.sourceVerified, true);
assert.equal(result.admissionComplete, true);
for (const name of ["conversation", "tool"]) {
  const category = result.categories.find(c => c.category === name);
  assert.equal(category.state, "present");
  assert.equal(category.observed_count, "0");
}
const usage = result.categories.find(c => c.category === "usage");
assert.equal(usage.usage.input_tokens, "9007199254740993");
assert.equal(usage.usage.ai_credits ?? "", process.argv[1]);
assert.equal(usage.state, process.argv[1] === "" ? "incomplete" : "present");
for (const mutate of [
  value => value.categories.push(value.categories[0]),
  value => value.categories[0].observed_count = -1,
  value => value.categories[0].category = "unknown",
  value => value.categories.find(c => c.category === "usage").usage.input_tokens = "9007199254740993",
  value => value.categories.find(c => c.category === "usage").usage.ai_credits = -1,
]) {
  const bad = JSON.parse(text);
  mutate(bad);
  assert.throws(() => parseRecordedCategories(JSON.stringify(bad), original.evidence_sha256, 200));
}
assert.throws(() => parseRecordedCategories(text.replace('"categories":', '"categories":[],"categories":'), original.evidence_sha256, 200));
`, wantCredits)
			node.Stdin = bytes.NewReader(response.Body.Bytes())
			output, err := node.CombinedOutput()
			require.NoError(t, err, string(output))
			document := get("/document")
			require.JSONEq(t, response.Body.String(), document.Body.String())
			require.Contains(t, document.Body.String(), `"input_tokens":9007199254740993`)
			export := get("/csv")
			rows, err := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 7)
			foundUsage, foundEmpty := false, false
			for _, row := range rows[1:] {
				if row[0] == "usage" {
					foundUsage = true
					require.Equal(t, "9007199254740993", row[7])
					require.Equal(t, wantCredits, row[12])
					if credits == nil {
						require.Equal(t, "incomplete", row[2])
					} else {
						require.Equal(t, "present", row[2])
					}
				}
				if row[0] == "conversation" {
					foundEmpty = true
					require.Equal(t, "0", row[5])
					require.Empty(t, row[12])
				}
			}
			require.True(t, foundUsage)
			require.True(t, foundEmpty)
			var downloaded struct {
				Categories []evidencestate.CategoryEvidence `json:"categories"`
			}
			require.NoError(t, json.Unmarshal(document.Body.Bytes(), &downloaded))
			require.Len(t, downloaded.Categories, 6)
		})
	}
}
