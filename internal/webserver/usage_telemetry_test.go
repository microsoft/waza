package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
)

func TestRecordUsageReportsFeatureCategoriesOnly(t *testing.T) {
	var mu sync.Mutex
	var features []any
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		mu.Lock()
		features = append(features, event["feature"])
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(collector.Close)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.URL, collector.Client(), true, false))
	policy := usagetelemetry.Resolve(usagetelemetry.Inputs{Getenv: func(string) string { return "" },
		SettingsPath: filepath.Join(t.TempDir(), "telemetry.json"), WorkDir: t.TempDir()})
	require.True(t, policy.Enabled, "%v", policy.Reasons)
	usagetelemetry.Start(policy, "1.0.0")

	handler := recordUsage(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, path := range []string{"/api/runs/secret", "/api/health", "/runs/secret", "/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusTeapot, rec.Code)
	}
	usagetelemetry.Shutdown(5 * time.Second)
	require.ElementsMatch(t, []any{"http_api", "dashboard"}, features)
}
