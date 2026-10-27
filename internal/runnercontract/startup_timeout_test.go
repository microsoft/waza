package runnercontract

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunnerTaskDeadlineDuringSanitizedNativeStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := fixture(t)
		request.Input.Eval = strings.Replace(request.Input.Eval, "timeout_seconds: 30", "timeout_seconds: 1", 1)
		engine := &instrumentedEngine{mode: "initialize-timeout"}
		service := NewService(WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
			return engine, nil
		}))
		result := service.Run(t.Context(), wire(t, request))
		require.Equal(t, Timeout, result.Outcome, "%+v", result.Diagnostics)
		require.Nil(t, result.Observation)
		require.Equal(t, 1, engine.initializes)
		require.Zero(t, engine.executes)
		require.Equal(t, 1, engine.shutdowns)
		require.NoError(t, ValidateResult(&result))
	})
}
