package trigger

import (
	"context"
	"io"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestUsesEvalMCPMatchesRunnerAdmissionEvenWithEmptyPrompts(t *testing.T) {
	require.False(t, UsesEvalMCP(nil, "copilot-sdk"))
	require.False(t, UsesEvalMCP(&TestSpec{}, "mock"))
	require.True(t, UsesEvalMCP(&TestSpec{}, "copilot-sdk"))
	spec := &models.EvalSpec{MCPMocks: []models.MCPMockConfig{{Name: "host", Fixtures: "missing-trigger-fixtures"}}}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir()))
	// A constructed Runner resolves MCP before it discovers that there are no
	// prompts. Do not infer dependency pruning from the prompt count.
	runner := NewRunner(&TestSpec{}, nil, cfg, io.Discard)
	_, _, err := runner.RunDetailed(context.Background())
	require.ErrorContains(t, err, "trigger MCP mock admission")
}
