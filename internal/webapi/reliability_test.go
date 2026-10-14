package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestReliabilityReadersAndAPI(t *testing.T) {
	const bootstrap = `{"lower":0.72,"upper":0.95,"mean":0.85,"confidence_level":0.95,"num_bootstraps":10000}`
	const passCI = `{"lower":0.2077,"upper":0.9385,"mean":0.6667,"confidence_level":0.95}`
	const successCI = `{"lower":0.0945,"upper":0.9055,"mean":0.5,"confidence_level":0.95}`
	passResponse := &ConfidenceIntervalResponse{Lower: 0.2077, Upper: 0.9385, Mean: 0.6667, ConfidenceLevel: 0.95}
	successResponse := &ConfidenceIntervalResponse{Lower: 0.0945, Upper: 0.9055, Mean: 0.5, ConfidenceLevel: 0.95}
	bootstrapResponse := &ConfidenceIntervalResponse{Lower: 0.72, Upper: 0.95, Mean: 0.85, ConfidenceLevel: 0.95}

	for _, tc := range []struct {
		name          string
		version       string
		summaryFields string
		taskFields    string
		otherFields   string
		otherTrials   *TrialCountsResponse
		statistics    *ReliabilitySummaryResponse
		passRateCI    *ConfidenceIntervalResponse
		bootstrapCI   *ConfidenceIntervalResponse
		trials        *TrialCountsResponse
	}{
		{
			name: "current", version: "1.5",
			summaryFields: `,"statistics":{"bootstrap_ci":` + bootstrap + `,"success_rate_ci":` + successCI + `,"pass_hat_k":[0.8333,0.6667,0.5]}`,
			taskFields:    `,"stats":{"total_runs":3,"passed_runs":2,"bootstrap_ci":` + bootstrap + `,"pass_rate_ci":` + passCI + `}`,
			otherFields:   `,"stats":{"total_runs":3,"passed_runs":3}`,
			otherTrials:   &TrialCountsResponse{Passed: 3, Total: 3},
			statistics:    &ReliabilitySummaryResponse{SuccessRateCI: successResponse, PassHatK: []float64{0.8333, 0.6667, 0.5}},
			passRateCI:    passResponse, bootstrapCI: bootstrapResponse,
			trials: &TrialCountsResponse{Passed: 2, Total: 3},
		},
		{
			name: "legacy-bootstrap-only", version: "1.4",
			summaryFields: `,"statistics":{"bootstrap_ci":` + bootstrap + `,"is_significant":true}`,
			taskFields:    `,"stats":{"total_runs":3,"passed_runs":2,"bootstrap_ci":` + bootstrap + `,"is_significant":true}`,
			bootstrapCI:   bootstrapResponse,
			trials:        &TrialCountsResponse{Passed: 2, Total: 3},
		},
		{name: "historical-no-stats", version: "1.0"},
		{
			name: "single-trial-no-interval", version: "1.5",
			taskFields: `,"stats":{"total_runs":1,"passed_runs":1}`,
			trials:     &TrialCountsResponse{Passed: 1, Total: 1},
		},
		{
			name: "reported-zero", version: "1.5",
			summaryFields: `,"statistics":{"success_rate_ci":{"lower":0,"upper":0.6576,"mean":0,"confidence_level":0.95},"pass_hat_k":[0,0]}`,
			taskFields:    `,"stats":{"total_runs":3,"passed_runs":0,"pass_rate_ci":{"lower":0,"upper":0.5615,"mean":0,"confidence_level":0.95}}`,
			statistics: &ReliabilitySummaryResponse{
				SuccessRateCI: &ConfidenceIntervalResponse{Lower: 0, Upper: 0.6576, Mean: 0, ConfidenceLevel: 0.95},
				PassHatK:      []float64{0, 0},
			},
			passRateCI: &ConfidenceIntervalResponse{Lower: 0, Upper: 0.5615, Mean: 0, ConfidenceLevel: 0.95},
			trials:     &TrialCountsResponse{Passed: 0, Total: 3},
		},
		{
			name: "interval-only", version: "1.5",
			summaryFields: `,"statistics":{"success_rate_ci":` + successCI + `}`,
			statistics:    &ReliabilitySummaryResponse{SuccessRateCI: successResponse},
		},
		{
			name: "curve-only", version: "1.5",
			summaryFields: `,"statistics":{"pass_hat_k":[0.5]}`,
			statistics:    &ReliabilitySummaryResponse{PassHatK: []float64{0.5}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, reader := range []string{"file", "storage-adapter"} {
				t.Run(reader, func(t *testing.T) {
					dir := t.TempDir()
					succeeded, failed, successRate := 1, 1, 0.5
					firstStatus, otherStatus := "failed", "passed"
					if tc.name == "reported-zero" {
						succeeded, failed, successRate = 0, 2, 0
						otherStatus = "failed"
					}
					if tc.name == "single-trial-no-interval" {
						firstStatus, otherStatus = "passed", "failed"
					}
					data := fmt.Sprintf(`{
						"schemaVersion":%q,"eval_id":"reliability","eval_name":"recorded",
						"summary":{"total_tests":2,"succeeded":%d,"failed":%d,"success_rate":%g%s},
						"tasks":[{"test_id":"one","display_name":"one","status":%q,"runs":[]%s},
							{"test_id":"two","display_name":"two","status":%q,"runs":[]%s}]
					}`, tc.version, succeeded, failed, successRate, tc.summaryFields, firstStatus, tc.taskFields, otherStatus, tc.otherFields)
					require.NoError(t, os.WriteFile(filepath.Join(dir, "reliability.json"), []byte(data), 0o600))
					var store RunStore = NewFileStore(dir)
					if reader == "storage-adapter" {
						store = NewStorageAdapter(storage.NewLocalStore(dir), "local")
					}
					detail, err := store.GetRun("reliability")
					require.NoError(t, err)
					require.Equal(t, tc.statistics, detail.Statistics)
					require.Len(t, detail.Tasks, 2)
					require.Equal(t, tc.passRateCI, detail.Tasks[0].PassRateCI)
					require.Equal(t, tc.bootstrapCI, detail.Tasks[0].BootstrapCI)
					require.Equal(t, tc.trials, detail.Tasks[0].Trials)
					require.Nil(t, detail.Tasks[1].PassRateCI)
					require.Equal(t, tc.otherTrials, detail.Tasks[1].Trials)

					mux := http.NewServeMux()
					RegisterRoutes(mux, store)
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/runs/reliability", nil))
					require.Equal(t, http.StatusOK, response.Code)
					var body map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
					require.Equal(t, tc.statistics != nil, body["statistics"] != nil)
					var tasks []map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(body["tasks"], &tasks))
					require.Equal(t, tc.passRateCI != nil, tasks[0]["passRateCI"] != nil)
					require.Equal(t, tc.trials != nil, tasks[0]["trials"] != nil)
					require.NotContains(t, tasks[1], "passRateCI")
					require.Equal(t, tc.otherTrials != nil, tasks[1]["trials"] != nil)
					require.NotContains(t, response.Body.String(), "isSignificant")
					require.NotContains(t, response.Body.String(), "is_significant")
					var wire RunDetail
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &wire))
					require.Equal(t, tc.statistics, wire.Statistics)
					require.Equal(t, tc.passRateCI, wire.Tasks[0].PassRateCI)
					require.Equal(t, tc.bootstrapCI, wire.Tasks[0].BootstrapCI)
					require.Equal(t, tc.trials, wire.Tasks[0].Trials)
					if tc.statistics != nil {
						var stats map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(body["statistics"], &stats))
						require.Equal(t, tc.statistics.SuccessRateCI != nil, stats["successRateCI"] != nil)
						require.Equal(t, len(tc.statistics.PassHatK) > 0, stats["passHatK"] != nil)
					}
				})
			}
		})
	}
}
