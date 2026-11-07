package runnerunit

import (
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func TestEvidenceTerminalUsesOneOriginalReportAndDetachedBinding(t *testing.T) {
	factories := 0
	service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	report := service.Run(t.Context(), wire(t, fixture(t)))
	require.NotNil(t, report.Observation)
	envelope, binding := report.Terminal()
	require.NotNil(t, binding)
	require.Equal(t, 1, report.Calls)
	require.Equal(t, 1, envelope.EngineCalls)
	original, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	binding.Identity.Run = "changed"
	binding.GraderIDs[0] = "changed"
	envelope.Identity.Run = "changed"
	envelope.Selection.Task = "changed"
	again, againBinding := report.Terminal()
	unchanged, err := runnerterminal.Encode(again, againBinding)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
	require.Equal(t, 1, report.Calls)
	require.Equal(t, 1, factories)
}

func TestEvidenceTerminalRejectedReportHasNoBindingOrExecution(t *testing.T) {
	report := Validate([]byte(`{}`))
	envelope, binding := report.Terminal()
	require.Nil(t, binding)
	require.Equal(t, 0, envelope.EngineCalls)
	_, err := runnerterminal.Encode(envelope, nil)
	require.NoError(t, err)
}
