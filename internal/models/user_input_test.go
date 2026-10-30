package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateUserInputResponses(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name      string
		responses []UserInputResponseConfig
		err       string
	}{
		{"empty list", []UserInputResponseConfig{}, ""},
		{"choice", []UserInputResponseConfig{{Question: "Q", Choice: s("a")}}, ""},
		{"choice within expected", []UserInputResponseConfig{{Question: "Q", Choices: &[]string{"a"}, Choice: s("a")}}, ""},
		{"empty freeform", []UserInputResponseConfig{{QuestionRegex: "Q.*", Freeform: s("")}}, ""},
		{"no question", []UserInputResponseConfig{{Choice: s("a")}}, "exactly one of question or question_regex"},
		{"blank question", []UserInputResponseConfig{{Question: "  ", Choice: s("a")}}, "question must not be blank"},
		{"both questions", []UserInputResponseConfig{{Question: "Q", QuestionRegex: "Q", Choice: s("a")}}, "exactly one of question or question_regex"},
		{"invalid regex", []UserInputResponseConfig{{QuestionRegex: "(", Choice: s("a")}}, "question_regex is invalid"},
		{"no answer", []UserInputResponseConfig{{Question: "Q"}}, "exactly one of choice or freeform"},
		{"both answers", []UserInputResponseConfig{{Question: "Q", Choice: s("a"), Freeform: s("b")}}, "exactly one of choice or freeform"},
		{"empty choice", []UserInputResponseConfig{{Question: "Q", Choice: s("")}}, "choice must not be empty"},
		{"choice outside expected", []UserInputResponseConfig{{Question: "Q", Choices: &[]string{"a"}, Choice: s("b")}}, "is not one of the expected choices"},
		{"second entry indexed", []UserInputResponseConfig{{Question: "Q", Choice: s("a")}, {Question: "R"}}, "user_input_responses[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUserInputResponses(tc.responses)
			if tc.err == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.err)
		})
	}
}

func TestValidateUserInputResponsesSchemaVersion(t *testing.T) {
	require.NoError(t, ValidateUserInputResponsesSchemaVersion(ScenarioSchemaVersion))
	for _, version := range []string{"", "1.0", "1.4", "1.99", "2.1", "3.0"} {
		require.ErrorContains(t, ValidateUserInputResponsesSchemaVersion(version), "requires explicit enclosing scenario schemaVersion 2.0", version)
	}
}

func TestLoadTestCasePreservesUserInputResponsesPresence(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		present bool
		count   int
	}{
		{"absent", "", false, 0},
		{"explicit empty", "user_input_responses: []\n", true, 0},
		{"entries", "user_input_responses:\n  - question: Pick?\n    choices: []\n    freeform: x\n", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "task.yaml")
			require.NoError(t, os.WriteFile(path, []byte("id: t\nname: T\ninputs:\n  prompt: go\n"+tc.field), 0o600))
			task, err := LoadTestCase(path)
			require.NoError(t, err)
			require.Equal(t, tc.present, task.UserInputResponses != nil)
			if !tc.present {
				return
			}
			require.Len(t, *task.UserInputResponses, tc.count)
			if tc.count > 0 {
				choices := (*task.UserInputResponses)[0].Choices
				require.NotNil(t, choices, "an explicit empty choices expectation must stay distinct from absent")
				require.Empty(t, *choices)
			}
			data, err := json.Marshal(task)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			require.Contains(t, wire, "user_input_responses")
		})
	}
}

func TestLoadTestCaseRejectsInvalidUserInputResponses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte("id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n  - question: Pick?\n"), 0o600))
	_, err := LoadTestCase(path)
	require.ErrorContains(t, err, "user_input_responses[0] must specify exactly one of choice or freeform")
}

func TestUserInputResponsesRequireCopilotExecutor(t *testing.T) {
	task := &TestCase{TestID: "t", UserInputResponses: &[]UserInputResponseConfig{}}
	require.NoError(t, task.ValidateForExecutor("copilot-sdk"))
	require.ErrorContains(t, task.ValidateForExecutor("mock"), "user_input_responses requires executor copilot-sdk")
	require.NoError(t, (&TestCase{TestID: "t"}).ValidateForExecutor("mock"))
}
