package execution

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/utils"
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

	toolCalls := coll.ToolCalls()
	require.Equal(t, []models.ToolCall{
		{
			Name:      "skill",
			Arguments: map[string]any{"skill": "example"},
			Success:   true,
			Result: &copilot.Result{
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
			Type: copilot.SessionError,
			Data: copilot.Data{
				Message: tc.Message,
			},
		})

		require.Equal(t, tc.Expected, coll.ErrorMessage())
	}
}
