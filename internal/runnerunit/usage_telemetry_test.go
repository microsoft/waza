package runnerunit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
)

func TestRunnerUnitCountsOneUsageExecutionPerAdmittedUnit(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(collector.Close)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.URL, collector.Client(), true, false))
	policy := usagetelemetry.Resolve(usagetelemetry.Inputs{Getenv: func(string) string { return "" },
		SettingsPath: filepath.Join(t.TempDir(), "telemetry.json"), WorkDir: t.TempDir()})
	require.True(t, policy.Enabled, "%v", policy.Reasons)
	usagetelemetry.Start(policy, "1.0.0")

	service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	request := fixture(t)
	require.Equal(t, "completed", service.Run(t.Context(), wire(t, request)).Status)
	request.Selection.Task = "neighbor"
	require.Equal(t, "rejected", service.Run(t.Context(), wire(t, request)).Status)
	usagetelemetry.Shutdown(5 * time.Second)

	require.Len(t, events, 1, "admission rejections are not executions")
	require.Equal(t, "execution_completed", events[0]["event"])
	require.Equal(t, "runner_unit", events[0]["surface"])
	require.Equal(t, "mock", events[0]["execution_mode"])
	require.Equal(t, "passed", events[0]["outcome"])
	require.Equal(t, "1", events[0]["task_count_bucket"])
}
