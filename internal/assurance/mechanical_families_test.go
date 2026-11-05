package assurance

import (
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func requireMechanicalVerdict(t *testing.T, parameters models.GraderParameters, evidence *graders.Context, kind models.GraderKind, passed bool, score float64, feedback string) {
	t.Helper()
	executor := &executionGuard{}
	evidence.Executor = executor
	observation := ObserveMechanical(t.Context(), referenceInput(parameters, evidence))
	require.Equal(t, Observed, observation.State)
	require.NoError(t, observation.Err)
	require.NotNil(t, observation.Result)
	require.Equal(t, "check", observation.Result.Name)
	require.Equal(t, kind, observation.Result.Type)
	require.Equal(t, passed, observation.Result.Passed)
	require.InDelta(t, score, observation.Result.Score, 1e-12)
	require.Contains(t, observation.Result.Feedback, feedback)
	require.Zero(t, executor.calls)
}

func TestObserveMechanicalJSONSchemaVerdicts(t *testing.T) {
	parameters := models.JSONSchemaGraderParameters{Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state": map[string]any{"enum": []any{"ready"}},
		},
		"required": []any{"state"},
	}}
	for _, candidate := range []struct {
		name     string
		output   string
		passed   bool
		feedback string
	}{
		{"good", `{"state":"ready"}`, true, "Output matches JSON schema"},
		{"alternative-valid", `{"note":"equivalent state","state":"ready"}`, true, "Output matches JSON schema"},
		{"bad-wrong-state", `{"state":"wrong"}`, false, "Schema validation failed"},
		{"bad-malformed-candidate-output", `not-json`, false, "Output is not valid JSON"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			score := 0.0
			if candidate.passed {
				score = 1
			}
			requireMechanicalVerdict(t, parameters, &graders.Context{Output: candidate.output},
				models.GraderKindJSONSchema, candidate.passed, score, candidate.feedback)
		})
	}
}

func TestObserveMechanicalBehaviorVerdicts(t *testing.T) {
	parameters := models.BehaviorGraderParameters{MaxToolCalls: 2, MaxTokens: 20}
	for _, candidate := range []struct {
		name         string
		calls        int
		inputTokens  int
		outputTokens int
		passed       bool
		score        float64
		feedback     string
	}{
		{"good", 1, 5, 5, true, 1, "All behavior checks passed"},
		{"alternative-valid-at-limits", 2, 17, 3, true, 1, "All behavior checks passed"},
		{"bad-too-many-calls", 3, 17, 3, false, 0.5, "Tool call count 3 exceeds max allowed 2"},
		{"bad-too-many-tokens", 2, 17, 4, false, 0.5, "Token usage 21 exceeds max allowed 20"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			evidence := &graders.Context{Session: &models.SessionDigest{
				ToolCallCount: candidate.calls,
				Usage: &models.UsageStats{
					InputTokens: candidate.inputTokens, OutputTokens: candidate.outputTokens,
				},
			}}
			requireMechanicalVerdict(t, parameters, evidence, models.GraderKindBehavior,
				candidate.passed, candidate.score, candidate.feedback)
		})
	}
}

func TestObserveMechanicalActionSequenceVerdicts(t *testing.T) {
	parameters := models.ActionSequenceGraderParameters{
		ExpectedActions: []string{"inspect", "edit"},
		MatchingMode:    models.ActionSequenceMatchingModeAnyOrder,
	}
	for _, candidate := range []struct {
		name     string
		actions  []string
		passed   bool
		score    float64
		feedback string
	}{
		{"good", []string{"inspect", "edit"}, true, 1, "Action sequence matched"},
		{"alternative-valid", []string{"edit", "inspect"}, true, 1, "Action sequence matched"},
		{"bad-missing-action", []string{"inspect"}, false, 2.0 / 3, "missing or insufficient actions: edit"},
		{"bad-wrong-action", []string{"inspect", "delete"}, false, 0.5, "missing or insufficient actions: edit"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			requireMechanicalVerdict(t, parameters, &graders.Context{Session: &models.SessionDigest{ToolsUsed: candidate.actions}},
				models.GraderKindActionSequence, candidate.passed, candidate.score, candidate.feedback)
		})
	}
}

func TestObserveMechanicalSkillInvocationVerdicts(t *testing.T) {
	parameters := models.SkillInvocationGraderParameters{
		RequiredSkills:  []string{"prepare", "edit"},
		ForbiddenSkills: []string{"unsafe"},
		Mode:            models.SkillMatchingModeAnyOrder,
	}
	for _, candidate := range []struct {
		name     string
		skills   []execution.SkillInvocation
		passed   bool
		score    float64
		feedback string
	}{
		{"good", []execution.SkillInvocation{{Name: "prepare"}, {Name: "edit"}}, true, 1, "Skill invocation sequence matched"},
		{"alternative-valid", []execution.SkillInvocation{{Name: "edit"}, {Name: "prepare"}}, true, 1, "Skill invocation sequence matched"},
		{"bad-missing-skill", []execution.SkillInvocation{{Name: "prepare"}}, false, 2.0 / 3, "missing or insufficient skills: edit"},
		{"bad-forbidden-skill", []execution.SkillInvocation{{Name: "prepare"}, {Name: "edit"}, {Name: "unsafe"}}, false, 0, "unsafe"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			requireMechanicalVerdict(t, parameters, &graders.Context{SkillInvocations: candidate.skills},
				models.GraderKindSkillInvocation, candidate.passed, candidate.score, candidate.feedback)
		})
	}
}
