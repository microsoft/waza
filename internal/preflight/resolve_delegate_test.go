package preflight

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/requirements"
	"github.com/stretchr/testify/require"
)

func TestResolveGraderCompatibilityDelegate(t *testing.T) {
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "same"}}}
	task := &models.TestCase{Validators: []models.ValidatorInline{{Identifier: "same"}},
		Checkpoints: []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "same"}}}}}
	for _, check := range []models.RequirementCheck{
		{Scope: "eval", Grader: "same"},
		{Scope: "task", Grader: "same"},
		{Scope: "checkpoint", Grader: "same", AfterTurn: 2},
		{Scope: "unknown", Grader: "same"},
		{Scope: "eval", Grader: "missing"},
		{Scope: "eval", Grader: ""},
	} {
		old, oldErr := ResolveGrader(check, task, spec)
		canonical, err := requirements.ResolveGrader(check, task, spec)
		require.Equal(t, canonical, old)
		if err == nil {
			require.NoError(t, oldErr)
			if canonical.Config != nil {
				require.Same(t, canonical.Config, old.Config)
			} else {
				require.Same(t, canonical.Inline, old.Inline)
			}
		} else {
			require.EqualError(t, oldErr, err.Error())
		}
	}
}
