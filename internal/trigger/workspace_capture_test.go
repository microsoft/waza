package trigger

import (
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestTriggerWorkspaceCaptureAdmission(t *testing.T) {
	for _, mode := range []string{"", "full", "none"} {
		t.Run(mode, func(t *testing.T) {
			spec := &models.EvalSpec{Config: models.Config{WorkspaceCapture: mode}}
			engine := &capturingEngine{}
			runner := NewRunner(&TestSpec{Skill: "example"}, engine, config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), nil)
			_, err := runner.testTrigger(t.Context(), "hello")
			require.NoError(t, err)
			require.Equal(t, mode == "none", engine.LastReq().SkipWorkspaceCapture)
		})
	}
	spec := &models.EvalSpec{Config: models.Config{WorkspaceCapture: "none"}, Graders: []models.GraderConfig{{
		Kind: models.GraderKindFile, Identifier: "file", Parameters: models.FileGraderParameters{MustExist: []string{"report"}},
	}}}
	engine := &capturingEngine{}
	runner := NewRunner(&TestSpec{Skill: "example"}, engine, config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), nil)
	_, err := runner.testTrigger(t.Context(), "hello")
	require.ErrorContains(t, err, "workspace_capture none")
	require.Nil(t, engine.LastReq())
}
