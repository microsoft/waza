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
		{"answer only", []UserInputResponseConfig{{Answer: s("a")}}, ""},
		{"empty answer", []UserInputResponseConfig{{Answer: s("")}}, ""},
		{"question and answer", []UserInputResponseConfig{{Question: "Deploy .*\\?", Answer: s("a")}}, ""},
		{"missing answer", []UserInputResponseConfig{{Question: "Q"}}, "user_input_responses[0].answer is required"},
		{"invalid regex", []UserInputResponseConfig{{Question: "(", Answer: s("a")}}, "question is not a valid regular expression"},
		{"second entry indexed", []UserInputResponseConfig{{Answer: s("a")}, {}}, "user_input_responses[1]"},
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

func TestCompileUserInputQuestionMatchesWholeQuestion(t *testing.T) {
	pattern, err := CompileUserInputQuestion("Deploy")
	require.NoError(t, err)
	require.True(t, pattern.MatchString("Deploy"))
	require.False(t, pattern.MatchString("Deploy where?"))
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
		{"entries", "user_input_responses:\n  - answer: ''\n  - question: Pick.*\n    answer: b\n", true, 2},
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
				first := (*task.UserInputResponses)[0]
				require.NotNil(t, first.Answer, "an explicit empty answer must stay distinct from a missing answer")
				require.Empty(t, *first.Answer)
				require.Empty(t, first.Question)
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
	require.ErrorContains(t, err, "user_input_responses[0].answer is required")
}

func TestUserInputResponsesRequireCopilotExecutor(t *testing.T) {
	task := &TestCase{TestID: "t", UserInputResponses: &[]UserInputResponseConfig{}}
	require.NoError(t, task.ValidateForExecutor("copilot-sdk"))
	require.ErrorContains(t, task.ValidateForExecutor("mock"), "user_input_responses requires executor copilot-sdk")
	require.NoError(t, (&TestCase{TestID: "t"}).ValidateForExecutor("mock"))
}
