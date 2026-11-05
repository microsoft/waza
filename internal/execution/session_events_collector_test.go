package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestNewSessionEventsCollector(t *testing.T) {
	reader, err := os.Open("../testdata/copilot_events_using_skill.json")
	require.NoError(t, err)

	defer reader.Close() //nolint:errcheck

	decoder := json.NewDecoder(reader)

	coll := NewSessionEventsCollector()

	i := -1
	for {
		i++
		var sessionEvent *copilot.SessionEvent
		err := decoder.Decode(&sessionEvent)

		if errors.Is(err, io.EOF) {
			break
		}

		require.NoErrorf(t, err, "error on iteration %d", i)
		coll.On(*sessionEvent)
	}

	require.Equal(t, 10, len(coll.SessionEvents()))
	require.Equal(t, []string{"", "yes"}, coll.OutputParts())
	require.Empty(t, coll.ErrorMessage())
	require.Equal(t, []SkillInvocation{{
		Name: "example",
		Path: "/home/\u003credacted\u003e/.copilot/skills/example/SKILL.md",
	}}, coll.SkillInvocations)

	toolCalls := coll.ToolCalls()
	require.Equal(t, []models.ToolCall{
		{
			ID:   "toolu_012vQAiz1EzkYvHcYVELY94P",
			Name: "skill",
			Arguments: models.ToolCallArgs{
				Skill: "example",
			},
			Success: true,
			Result: &copilot.ToolExecutionCompleteResult{
				Content:         "Skill \"example\" loaded successfully. Follow the instructions in the skill context.",
				DetailedContent: utils.Ptr("Skill loaded successfully ✅\n\n---\nname: example\ndescription: \"Checks to see if skills are enabled - if you use this skill it prints out yes\"\n---\n"),
			},
		},
	}, toolCalls)

	select {
	case <-coll.Done():
	default:
		require.Fail(t, "Should have been Done()")
	}
}

func TestNewSessionEventsCollector_Error(t *testing.T) {
	tests := []struct {
		Message  *string
		Expected string
	}{
		{Message: utils.Ptr(""), Expected: sessionFailedUnknown},
		{Message: nil, Expected: sessionFailedUnknown},
		{Message: utils.Ptr("an error message"), Expected: "an error message"},
	}

	for _, tc := range tests {
		coll := NewSessionEventsCollector()

		// this isn't something I've been able to trigger, but our assumption is that when it's happened
		// we should set the 'ErrorMsg' field.

		coll.On(copilot.SessionEvent{
			Data: &copilot.SessionErrorData{Message: derefTestString(tc.Message)},
		})

		require.Equal(t, tc.Expected, coll.ErrorMessage())
	}
}

func TestSessionEventsCollectorSuppressesAllIntentToolShellOutput(t *testing.T) {
	coll := NewSessionEventsCollector()
	const intentCallID = "intent-call"

	coll.On(copilot.SessionEvent{
		Data: &copilot.ToolExecutionStartData{
			ToolCallID: intentCallID,
			ToolName:   "report_intent",
		},
	})
	for _, text := range []string{"first chunk", "second chunk"} {
		coll.On(copilot.SessionEvent{
			Data: &copilot.ToolShellOutputData{
				Text:       text,
				ToolCallID: intentCallID,
			},
		})
	}

	require.Empty(t, coll.SessionEvents(), "intent start and every shell-output chunk must be suppressed")
	require.True(t, coll.intentToolIDs[intentCallID], "shell output must not clear the intent filter")

	coll.On(copilot.SessionEvent{
		Data: &copilot.ToolExecutionCompleteData{ToolCallID: intentCallID},
	})
	require.Empty(t, coll.SessionEvents(), "intent completion must be suppressed")
	require.False(t, coll.intentToolIDs[intentCallID], "completion must clear the intent filter")

	coll.On(copilot.SessionEvent{
		Data: &copilot.ToolShellOutputData{
			Text:       "unrelated output",
			ToolCallID: "ordinary-call",
		},
	})
	require.Len(t, coll.SessionEvents(), 1, "ordinary tool output must continue to be collected")
}

func TestSessionEventsCollectorPreservesLegacyPartialResultFixture(t *testing.T) {
	wireEvent, err := os.ReadFile("../testdata/legacy_tool_execution_partial_result.json")
	require.NoError(t, err)

	var event copilot.SessionEvent
	require.NoError(t, json.Unmarshal(wireEvent, &event))
	require.Equal(t, copilot.SessionEventType("tool.execution_partial_result"), event.Type())

	coll := NewSessionEventsCollector()
	coll.On(event)
	require.Len(t, coll.SessionEvents(), 1)

	text, toolCallID, ok := copilotevents.ToolOutput(coll.SessionEvents()[0])
	require.True(t, ok)
	require.Equal(t, "legacy shell output", text)
	require.Equal(t, "legacy-call-123", toolCallID)

	persisted, err := json.Marshal(models.TranscriptEvent{SessionEvent: coll.SessionEvents()[0]})
	require.NoError(t, err)

	var restored models.TranscriptEvent
	require.NoError(t, json.Unmarshal(persisted, &restored))
	require.Equal(t, event.Type(), restored.Type())

	text, toolCallID, ok = copilotevents.ToolOutput(restored.SessionEvent)
	require.True(t, ok)
	require.Equal(t, "legacy shell output", text)
	require.Equal(t, "legacy-call-123", toolCallID)
}

func TestSessionEventsCollector_OnSkillInvokedCallback(t *testing.T) {
	skillName := "test-skill"
	skillPath := "/skills/test-skill/SKILL.md"

	t.Run("fires callback when skill invoked", func(t *testing.T) {
		coll := NewSessionEventsCollector()
		var captured SkillInvocation
		coll.SetOnSkillInvoked(func(si SkillInvocation) {
			captured = si
		})

		coll.On(copilot.SessionEvent{
			Data: &copilot.SkillInvokedData{
				Name: skillName,
				Path: skillPath,
			},
		})

		require.Equal(t, skillName, captured.Name)
		require.Equal(t, skillPath, captured.Path)
	})

	t.Run("no callback set does not panic", func(t *testing.T) {
		coll := NewSessionEventsCollector()

		require.NotPanics(t, func() {
			coll.On(copilot.SessionEvent{
				Data: &copilot.SkillInvokedData{
					Name: skillName,
					Path: skillPath,
				},
			})
		})

		require.Len(t, coll.SkillInvocations, 1)
	})

	t.Run("callback fires for each invocation", func(t *testing.T) {
		coll := NewSessionEventsCollector()
		count := 0
		coll.SetOnSkillInvoked(func(_ SkillInvocation) {
			count++
		})

		for i := 0; i < 3; i++ {
			name := fmt.Sprintf("skill-%d", i)
			coll.On(copilot.SessionEvent{
				Data: &copilot.SkillInvokedData{Name: name},
			})
		}

		require.Equal(t, 3, count)
		require.Len(t, coll.SkillInvocations, 3)
	})
}

func derefTestString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
