package requirements

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestResolveGraderBorrowsNativeDeclarationWithoutDefaults(t *testing.T) {
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "same", Ref: "preserved", Weight: 0}}}
	task := &models.TestCase{
		Validators:  []models.ValidatorInline{{Identifier: "same", Rubric: "preserved", Weight: 0}},
		Checkpoints: []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "same", Checks: []string{"preserved"}}}}},
	}
	for _, test := range []struct {
		check  models.RequirementCheck
		config *models.GraderConfig
		inline *models.ValidatorInline
	}{
		{models.RequirementCheck{Scope: "eval", Grader: "same"}, &spec.Graders[0], nil},
		{models.RequirementCheck{Scope: "task", Grader: "same"}, nil, &task.Validators[0]},
		{models.RequirementCheck{Scope: "checkpoint", Grader: "same", AfterTurn: 2}, nil, &task.Checkpoints[0].Graders[0]},
	} {
		t.Run(test.check.Scope, func(t *testing.T) {
			got, err := ResolveGrader(test.check, task, spec)
			require.NoError(t, err)
			if test.config != nil {
				require.Same(t, test.config, got.Config)
				require.Nil(t, got.Inline)
			} else {
				require.Same(t, test.inline, got.Inline)
				require.Nil(t, got.Config)
			}
		})
	}
	require.Zero(t, spec.Graders[0].Weight)
	require.Zero(t, task.Validators[0].Weight)
	require.Equal(t, "preserved", spec.Graders[0].Ref)
	require.Equal(t, []string{"preserved"}, task.Checkpoints[0].Graders[0].Checks)
}

func TestResolveGraderRejectsAmbiguousScopesWithoutBorrowing(t *testing.T) {
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "same"}, {Identifier: "same"}}}
	task := &models.TestCase{
		Validators: []models.ValidatorInline{{Identifier: "same"}, {Identifier: "same"}},
		Checkpoints: []models.Checkpoint{
			{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "same"}}},
			{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "same"}}},
		},
	}
	for _, scope := range []string{"eval", "task", "checkpoint"} {
		check := models.RequirementCheck{Scope: scope, Grader: "same"}
		if scope == "checkpoint" {
			check.AfterTurn = 2
		}
		got, err := ResolveGrader(check, task, spec)
		require.ErrorContains(t, err, "resolves to 2 declarations")
		require.Nil(t, got.Config)
		require.Nil(t, got.Inline)
	}
}
