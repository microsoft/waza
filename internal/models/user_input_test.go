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

func TestCompileUserInputQuestionRejectsAnchorEscape(t *testing.T) {
	// Wrapped naively, this unbalanced pattern compiles as ^(?:Deploy)|(?:Delete)$
	// and would match any question starting with "Deploy".
	_, err := CompileUserInputQuestion("Deploy)|(?:Delete")
	require.Error(t, err)
	require.ErrorContains(t, ValidateUserInputResponses([]UserInputResponseConfig{{Question: "Deploy)|(?:Delete", Answer: new("a")}}),
		"question is not a valid regular expression")
}

func TestConfigValidateNativeUserInput(t *testing.T) {
	require.NoError(t, Config{}.ValidateNativeUserInput("1.4"), "the flag is off by default")
	on := Config{NativeUserInput: true, EngineType: "copilot-sdk"}
	require.NoError(t, on.ValidateNativeUserInput(ScenarioSchemaVersion))
	for _, version := range []string{"", "1.4", "1.99", "2.1"} {
		require.ErrorContains(t, on.ValidateNativeUserInput(version), "requires a scenario eval with schemaVersion 2.0", version)
	}
	require.ErrorContains(t, Config{NativeUserInput: true, EngineType: "mock"}.ValidateNativeUserInput(ScenarioSchemaVersion), "requires executor copilot-sdk")
}

func TestValidateTaskUserInputRequiresEvalFlag(t *testing.T) {
	scripted := &TestCase{TestID: "t", UserInputResponses: &[]UserInputResponseConfig{}}
	require.ErrorContains(t, ValidateTaskUserInput(Config{}, scripted), "user_input_responses requires config.native_user_input: true")
	require.NoError(t, ValidateTaskUserInput(Config{NativeUserInput: true}, scripted))
	require.NoError(t, ValidateTaskUserInput(Config{}, &TestCase{TestID: "t"}))
	invalid := &TestCase{TestID: "t", UserInputResponses: &[]UserInputResponseConfig{{Question: "("}}}
	require.ErrorContains(t, ValidateTaskUserInput(Config{NativeUserInput: true}, invalid), "answer is required")
}

func TestLoadTestCaseRejectsExplicitEmptyQuestion(t *testing.T) {
	for _, value := range []string{`""`, `''`, "null", "~", "5", "[a]"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "task.yaml")
			task := "id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n  - question: " + value + "\n    answer: a\n"
			require.NoError(t, os.WriteFile(path, []byte(task), 0o600))
			_, err := LoadTestCase(path)
			require.Error(t, err)
			if value != "[a]" {
				require.ErrorContains(t, err, "user_input_responses[0].question must be a non-empty string")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte("id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n  - answer: a\n  - question: \"Q.*\"\n    answer: b\n"), 0o600))
	task, err := LoadTestCase(path)
	require.NoError(t, err, "omitting question remains the wildcard")
	require.Empty(t, (*task.UserInputResponses)[0].Question)
	require.Equal(t, "Q.*", (*task.UserInputResponses)[1].Question)
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

func TestLoadTestCaseResolvesMergedQuestionAndAnswer(t *testing.T) {
	load := func(t *testing.T, entries string) (*TestCase, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "task.yaml")
		require.NoError(t, os.WriteFile(path, []byte("id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n"+entries), 0o600))
		return LoadTestCase(path)
	}
	for name, entries := range map[string]string{
		"merged null question":  "  - <<: {question: null, answer: approve}\n",
		"merged empty question": "  - <<: {question: \"\"}\n    answer: approve\n",
		"merged through anchor": "  - &base {answer: a}\n  - <<: [*base, {question: ~}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, entries)
			require.ErrorContains(t, err, "question must be a non-empty string")
		})
	}
	for name, entries := range map[string]string{
		"bool answer":        "  - answer: false\n",
		"int answer":         "  - answer: 5\n",
		"merged bool answer": "  - <<: {answer: true}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, entries)
			require.ErrorContains(t, err, "answer must be a string")
		})
	}
	t.Run("explicit question overrides merged null", func(t *testing.T) {
		task, err := load(t, "  - <<: {question: null, answer: approve}\n    question: \"Approve.*\"\n")
		require.NoError(t, err)
		require.Equal(t, "Approve.*", (*task.UserInputResponses)[0].Question)
		require.Equal(t, "approve", *(*task.UserInputResponses)[0].Answer)
	})
	t.Run("merged string values are accepted", func(t *testing.T) {
		task, err := load(t, "  - <<: {question: \"Deploy.*\", answer: staging}\n")
		require.NoError(t, err)
		require.Equal(t, "Deploy.*", (*task.UserInputResponses)[0].Question)
	})
	t.Run("quoted string answer", func(t *testing.T) {
		task, err := load(t, "  - answer: \"false\"\n")
		require.NoError(t, err)
		require.Equal(t, "false", *(*task.UserInputResponses)[0].Answer)
	})
}
