package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityHistoricalDashboard(t *testing.T) {
	root := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1")
	for _, c := range []struct {
		name, file string
		tokens     int
		credits    *float64
	}{
		{"legacy", "results-1.0.json", 0, nil},
		{"cached", "results-1.4.json", 0, nil},
		{"legacy-usage", "results-1.4.json", 120, utils.Ptr(5.0)},
		{"current-usage", "results-1.4.json", 30, utils.Ptr(0.0)},
	} {
		t.Run(c.name, func(t *testing.T) {
			outcome, err := models.LoadEvaluationOutcome(filepath.Join(root, c.file))
			require.NoError(t, err)
			if c.name == "legacy-usage" {
				outcome.EvaluationUsage = nil
			}
			if c.name == "current-usage" {
				outcome.Digest.Usage = &models.UsageStats{InputTokens: 20, OutputTokens: 10, AICredits: utils.Ptr(0.0)}
			}
			dir := t.TempDir()
			writeOutcomeFile(t, filepath.Join(dir, "historical.json"), *outcome)
			store := NewFileStore(dir)
			h := NewHandlers(store)
			req := httptest.NewRequest(http.MethodGet, "/api/runs/compatibility-run", nil)
			req.SetPathValue("id", "compatibility-run")
			rec := httptest.NewRecorder()
			h.HandleRunDetail(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			var detail RunDetail
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
			require.Equal(t, "compatibility-run", detail.ID)
			require.Equal(t, c.tokens, detail.Tokens)
			require.Equal(t, c.credits, detail.AICredits)
			require.Len(t, detail.Tasks, 1)
			require.Equal(t, "Compatibility task", detail.Tasks[0].Name)
			require.Equal(t, "passed", detail.Tasks[0].Outcome)
			require.Equal(t, "answer", detail.Tasks[0].GraderResults[0].Name)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wire))
			_, hasCredits := wire["aiCredits"]
			require.Equal(t, c.credits != nil, hasCredits, "unavailable is omitted, reported zero is numeric")
			if c.name == "legacy" {
				expected, err := os.ReadFile(filepath.Join(root, "dashboard-legacy.json"))
				require.NoError(t, err)
				require.JSONEq(t, string(expected), rec.Body.String(), "same fixed API fixture is consumed by Playwright")
			}
			data, err := os.ReadFile(filepath.Join(dir, "historical.json"))
			require.NoError(t, err)
			reloaded, err := models.ParseEvaluationOutcome(data, "corpus")
			require.NoError(t, err)
			require.Equal(t, outcome.TestOutcomes, reloaded.TestOutcomes, "API must not rewrite historical diagnostics")
		})
	}
}
