package evidence

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRequirementObservationsRespectScopeAndOperationalUncertainty(t *testing.T) {
	for _, test := range []struct {
		name, scope                             string
		kind                                    models.GraderKind
		collision, missing, operational, passed bool
		observation, category                   string
	}{
		{"recorded failure", "task", models.GraderKindText, false, false, false, false, "grader_recorded_failure", "grader_observation"},
		{"alternative valid output", "task", models.GraderKindText, false, false, false, true, "grader_recorded_pass", "grader_observation"},
		{"eval scope", "eval", models.GraderKindText, false, false, false, false, "grader_recorded_failure", "grader_observation"},
		{"collision", "task", models.GraderKindText, true, false, false, true, "unresolved", "insufficient_evidence"},
		{"missing", "task", models.GraderKindText, false, true, false, false, "unresolved", "insufficient_evidence"},
		{"unknown scope", "other", models.GraderKindText, false, false, false, true, "unresolved", "insufficient_evidence"},
		{"judge", "task", models.GraderKindPrompt, false, false, false, true, "grader_recorded_pass", "qualitative_observation"},
		{"executable", "task", models.GraderKindProgram, false, false, false, false, "grader_recorded_failure", "operational_status_unavailable"},
		{"unpreserved state", "task", models.GraderKindFile, false, false, false, false, "grader_recorded_failure", "insufficient_evidence"},
		{"operational error", "task", models.GraderKindText, false, false, true, true, "grader_recorded_pass", "operational_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := &models.TestCase{
				TestID:       "task",
				Requirements: []models.Requirement{{ID: "req", Checks: []models.RequirementCheck{{Scope: test.scope, Grader: "check"}}}},
				Validators:   []models.ValidatorInline{{Identifier: "check", Kind: test.kind}},
			}
			spec := &models.EvalSpec{}
			if test.scope == "eval" || test.collision {
				spec.Graders = []models.GraderConfig{{Identifier: "check", Kind: test.kind}}
				if !test.collision {
					task.Validators = nil
				}
			}
			run := &models.RunResult{Validations: map[string]models.GraderResults{"check": {Passed: test.passed}}, Status: models.StatusPassed}
			if test.missing {
				run.Validations = nil
			}
			if test.operational {
				run.Status = models.StatusError
			}
			explanations := ExplainRequirements(spec, task, run)
			require.Len(t, explanations, 1)
			check := explanations[0].Checks[0]
			require.Equal(t, test.observation, check.Observation)
			require.Equal(t, test.category, check.Category)
			require.Empty(t, check.References)
		})
	}
}

func TestCheckpointAndUncoveredRequirementObservations(t *testing.T) {
	task := &models.TestCase{TestID: "task",
		Requirements: []models.Requirement{
			{ID: "checkpoint", Checks: []models.RequirementCheck{{Scope: "checkpoint", Grader: "check", AfterTurn: 2}}},
			{ID: "uncovered"},
		},
		Checkpoints: []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindText}}}},
	}

	run := &models.RunResult{Checkpoints: []models.CheckpointOutcome{{AfterTurn: 2, Validations: map[string]models.GraderResults{"check": {Passed: false}}}}}
	explanations := ExplainRequirements(nil, task, run)
	require.Equal(t, "grader_recorded_failure", explanations[0].Checks[0].Observation)
	require.Equal(t, "unresolved", explanations[1].Checks[0].Observation)
	run.Checkpoints = append(run.Checkpoints, run.Checkpoints[0])
	require.Equal(t, "unresolved", ExplainRequirements(nil, task, run)[0].Checks[0].Observation)
	task.Requirements[0].Checks[0].AfterTurn = 3
	require.Equal(t, "unresolved", ExplainRequirements(nil, task, run)[0].Checks[0].Observation)
}

func TestRegradingDoesNotReassessHistoricalCheckpoint(t *testing.T) {
	task := &models.TestCase{TestID: "task",
		Requirements: []models.Requirement{{ID: "checkpoint", Checks: []models.RequirementCheck{{Scope: "checkpoint", Grader: "check", AfterTurn: 1}}}},
		Checkpoints:  []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{RegexMatch: []string{"new criterion"}}}}}},
	}
	manifest := sampleManifest(t)
	manifest.Artifacts[0].ID = "checkpoints"
	require.NoError(t, Seal(manifest))
	run := &models.RunResult{Evidence: manifest, Checkpoints: []models.CheckpointOutcome{{AfterTurn: 1, Validations: map[string]models.GraderResults{"check": {Passed: true}}}}}
	var err error
	run.Evidence, err = Regrade(manifest)
	require.NoError(t, err)
	check := ExplainRequirements(nil, task, run)[0].Checks[0]
	require.Equal(t, "unresolved", check.Observation)
	require.Empty(t, check.References)
	require.Equal(t, "unavailable", run.Evidence.Artifacts[0].Availability)
	run.Evidence = nil
	check = ExplainRegradedRequirements(nil, task, run)[0].Checks[0]
	require.Equal(t, "unresolved", check.Observation)
	require.Len(t, run.Checkpoints, 1)
}
