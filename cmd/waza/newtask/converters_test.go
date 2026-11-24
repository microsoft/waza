package newtask

import (
	"context"
	"path/filepath"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestCreateTestCaseFromCopilotLog_UsingSkillFixture(t *testing.T) {
	testCopilotLog := filepath.Join("..", "..", "..", "internal", "testdata", "copilot_events_using_skill.json")

	tc, err := CreateTestCaseFromCopilotLog(testCopilotLog, &CreateTestCaseFromCopilotLogOptions{
		DisplayName: "fixture-case",
		TestID:      "fixture-id",
		Tags:        []string{"from-fixture"},
	})

	require.NoError(t, err)

	expected := &models.TestCase{
		DisplayName: "fixture-case",
		TestID:      "fixture-id",
		Tags:        []string{"from-fixture"},
		Stimulus: models.TaskStimulus{
			Message: "use the example horn",
		},
		Validators: []models.ValidatorInline{
			{
				Identifier: "skills-check",
				Kind:       models.GraderKindSkillInvocation,
				Parameters: models.SkillInvocationGraderParameters{
					RequiredSkills: []string{"example"},
					Mode:           models.SkillMatchingModeAnyOrder,
				},
			},
			{
				Identifier: "tools-check",
				Kind:       models.GraderKindToolConstraint,
				Parameters: models.ToolConstraintGraderParameters{
					ExpectTools: []models.ToolSpecParameters{{
						Tool:         "skill",
						SkillPattern: "example",
					}},
				},
			},
			{
				Identifier: "check-response",
				Kind:       models.GraderKindText,
				Parameters: models.TextGraderParameters{
					ContainsCS: []string{"yesyes"}, // response from the assistant in our test file
				},
			},
		},
	}

	require.Equal(t, expected, tc)
}

func TestCreateTestCaseFromCopilotEvents_UsesFinalAssistantMessage(t *testing.T) {
	logPath := filepath.Join("..", "..", "..", "internal", "testdata", "copilot_events_using_skill.json")
	var events []copilot.SessionEvent
	for event, err := range utils.NewCopilotLogIterator(logPath) {
		require.NoError(t, err)
		events = append(events, event)
	}
	options := &CreateTestCaseFromCopilotLogOptions{
		DisplayName: "fixture-case",
		TestID:      "fixture-id",
		Tags:        []string{"from-fixture"},
	}
	actual, err := CreateTestCaseFromCopilotEvents(events, options)
	require.NoError(t, err)
	require.Len(t, actual.Validators, 3)
	responseCheck := actual.Validators[2]
	require.Equal(t, "check-response", responseCheck.Identifier)
	params, ok := responseCheck.Parameters.(models.TextGraderParameters)
	require.True(t, ok)
	require.Equal(t, []string{"yes"}, params.ContainsCS)

	grader, err := graders.Create(responseCheck.Identifier, params)
	require.NoError(t, err)
	result, err := grader.Grade(context.Background(), &graders.Context{Output: "yes"})
	require.NoError(t, err)
	require.True(t, result.Passed)
}
