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
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
)

// startUsageCollector activates usage telemetry against a local fake collector
// and returns a func that flushes and reports the received events.
func startUsageCollector(t *testing.T) func() []map[string]any {
	t.Helper()
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
	t.Cleanup(func() { usagetelemetry.Shutdown(0) })
	return func() []map[string]any {
		usagetelemetry.Shutdown(5 * time.Second)
		mu.Lock()
		defer mu.Unlock()
		return events
	}
}

func TestRunnerUnitCountsOneUsageExecutionPerAdmittedUnit(t *testing.T) {
	flush := startUsageCollector(t)

	service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	request := fixture(t)
	require.Equal(t, "completed", service.Run(t.Context(), wire(t, request)).Status)
	request.Selection.Task = "neighbor"
	require.Equal(t, "rejected", service.Run(t.Context(), wire(t, request)).Status)
	events := flush()

	require.Len(t, events, 1, "admission rejections are not executions")
	require.Equal(t, "execution_completed", events[0]["event"])
	require.Equal(t, "runner_unit", events[0]["surface"])
	require.Equal(t, "mock", events[0]["execution_mode"])
	require.Equal(t, "passed", events[0]["outcome"])
	require.Equal(t, "1", events[0]["task_count_bucket"])
}

func TestSubmitCountsOneUsageExecutionAndNoneForReuse(t *testing.T) {
	flush := startUsageCollector(t)
	backend := submissionBackend(t)
	service := NewService(nil)
	request := fixture(t)
	first, err := service.Submit(t.Context(), submissionWire(t, request, nil), backend, SubmissionHooks{})
	require.NoError(t, err)
	require.Equal(t, "completed", first.Status)
	source := executionattempt.Source{ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest}
	reused, err := service.Submit(t.Context(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}),
		backend, SubmissionHooks{})
	require.NoError(t, err)
	require.Equal(t, "reused", reused.Status)

	events := flush()
	require.Len(t, events, 1, "fresh submissions count once; reuse does not execute")
	require.Equal(t, "runner_unit", events[0]["surface"])
}
