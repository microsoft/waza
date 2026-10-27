package orchestration

import (
	"fmt"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLocalSingleUnitUsesSharedFrozenBoundary(t *testing.T) {
	eval, err := os.ReadFile("../runnercontract/testdata/local/eval-1.0.yaml")
	require.NoError(t, err)
	source, err := os.ReadFile("../runnercontract/testdata/local/task.yaml")
	require.NoError(t, err)
	spec, err := models.ParseCapturedEvalSpecOffline(eval, "eval.yaml")
	require.NoError(t, err)
	task, err := models.ParseCapturedTestCaseOffline(source, "task.yaml", func(string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected external source read")
	})
	require.NoError(t, err)
	engine := execution.NewMockEngine(spec.Config.ModelID)
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
	runner := NewEvalRunner(config.NewEvalConfig(spec), engine)
	runner.singleUnit = true
	runner.evalRunID = "historical-local-identity"
	mapped, handled := runner.adaptLocalUnit(t.Context(), task, 1, 1)
	require.True(t, handled, "eligible local run must not retain a parallel implementation")
	before := runner.executeAttemptWithRequest(t.Context(), task, 1, 1, nil, false)
	require.Equal(t, before.Status, mapped.Status)
	require.Equal(t, before.FinalOutput, mapped.FinalOutput)
	require.Equal(t, before.RunNumber, mapped.RunNumber)
	require.Equal(t, before.Attempts, mapped.Attempts)
	require.Equal(t, before.Validations["_output_contains"].Passed, mapped.Validations["_output_contains"].Passed)
	require.Equal(t, before.Validations["_output_contains"].Score, mapped.Validations["_output_contains"].Score)
	require.Equal(t, "historical-local-identity", runner.evalRunID)
	runner.singleUnit = false
	_, handled = runner.adaptLocalUnit(t.Context(), task, 1, 1)
	require.False(t, handled, "batch execution is not reinterpreted as a unit")
	runner.singleUnit = true
	runner.skipGraders = true
	_, handled = runner.adaptLocalUnit(t.Context(), task, 1, 1)
	require.False(t, handled, "execution-only local behavior remains unchanged")
}
