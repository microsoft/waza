package graders

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestValidateConfigMatchesLocalFactory(t *testing.T) {
	for _, params := range []models.GraderParameters{
		models.TextGraderParameters{Contains: []string{"ready"}},
		models.FileGraderParameters{MustExist: []string{"artifact.json"}},
		models.BehaviorGraderParameters{},
		models.ActionSequenceGraderParameters{ExpectedActions: []string{"read"}, MatchingMode: models.ActionSequenceMatchingModeAnyOrder},
		models.ActionSequenceGraderParameters{ExpectedActions: []string{"read"}, MatchingMode: "unknown"},
		models.SkillInvocationGraderParameters{RequiredSkills: []string{"example"}},
		models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "read"}}},
		models.ToolCallsGraderParameters{},
		models.ToolCallsGraderParameters{RequiredTools: []string{"read"}},
		models.DiffGraderParameters{ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "file", Contains: []string{"ready"}}}},
		models.PromptGraderParameters{},
		models.JSONSchemaGraderParameters{Schema: map[string]any{"type": "object"}},
		models.ProgramGraderParameters{Command: "never-executed"},
		models.TriggerHeuristicGraderParameters{SkillPath: "does-not-exist"},
		models.GenericGraderParameters{},
		nil,
	} {
		_, factoryErr := Create("check", params)
		validateErr := ValidateConfig("check", params)
		require.Equal(t, factoryErr != nil, validateErr != nil)
	}
}

func TestValidateConfigInlineWithoutInterpreter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, language := range []models.Language{"", models.LanguagePython, models.LanguageJavascript} {
		require.NoError(t, ValidateConfig("script", models.InlineScriptGraderParameters{
			Language: language, Assertions: []string{"syntax and evaluation remain unresolved"},
		}))
	}
	require.Error(t, ValidateConfig("script", models.InlineScriptGraderParameters{Language: "unknown"}))
	require.Error(t, ValidateConfig("script", models.InlineScriptGraderParameters{}))
}

func TestValidateConfigRejectsDeferredInvalidChecks(t *testing.T) {
	for _, params := range []models.GraderParameters{
		models.TextGraderParameters{},
		models.TextGraderParameters{Contains: []string{""}},
		models.TextGraderParameters{NotContains: []string{""}},
		models.TextGraderParameters{RegexMatch: []string{""}},
		models.FileGraderParameters{MustExist: []string{""}},
		models.FileGraderParameters{MustNotExist: []string{""}},
		models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "file", MustMatch: []string{""}}}},
		models.TextGraderParameters{RegexMatch: []string{"["}},
		models.TextGraderParameters{RegexNotMatch: []string{"["}},
		models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "file", MustMatch: []string{"["}}}},
		models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "file", MustNotMatch: []string{"["}}}},
		models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "file"}}},
		models.JSONSchemaGraderParameters{Schema: map[string]any{"type": "invalid"}},
	} {
		require.Error(t, ValidateConfig("check", params))
		_, err := Create("check", params)
		require.NoError(t, err, "legacy runtime constructors retain their deferred checks")
	}
	require.NoError(t, ValidateConfig("check", models.FileGraderParameters{
		ContentPatterns: []models.FileContentPatternParameters{{Path: "file", MustMatch: []string{"valid"}}},
	}))
	require.NoError(t, ValidateConfig("check", models.JSONSchemaGraderParameters{SchemaFile: "not-read-by-static-validation.json"}))
	require.NoError(t, ValidateConfig("check", models.TriggerHeuristicGraderParameters{SkillPath: "not-read", Mode: "positive"}))
	require.NoError(t, ValidateConfig("check", models.PromptGraderParameters{Rubric: "not-read.md"}))
	require.NoError(t, ValidateConfig("check", models.TextGraderParameters{Contains: []string{" "}}), "literal whitespace can be a meaningful format check")
}
