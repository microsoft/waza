package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const capturedEvalFixture = `schemaVersion: "1.0"
name: inert
config: {executor: copilot-sdk, model: offline, trials_per_task: 1, timeout_seconds: 30}
tasks: [task.yaml]
graders: [{type: text, name: text, config: {contains: [hello]}}]
`

func TestCapturedOfflineNativeParserEquivalence(t *testing.T) {
	base := t.TempDir()
	eval := []byte(capturedEvalFixture)
	path := filepath.Join(base, "eval.yaml")
	require.NoError(t, os.WriteFile(path, eval, 0600))
	legacy, err := LoadEvalSpecOffline(path)
	require.NoError(t, err)
	actual, err := ParseCapturedEvalSpecOffline(eval, path)
	require.NoError(t, err)
	require.Equal(t, legacy, actual)

	task := []byte("id: task\ninputs:\n  prompt_file: prompt.txt\n  context: {large: 9007199254740993}\n")
	path = filepath.Join(base, "task.yaml")
	require.NoError(t, os.WriteFile(path, task, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "prompt.txt"), []byte("hello"), 0600))
	legacyTask, err := LoadTestCaseOffline(path)
	require.NoError(t, err)
	calls := 0
	actualTask, err := ParseCapturedTestCaseOffline(task, path, func(requested string) ([]byte, error) {
		calls++
		require.Equal(t, filepath.Join(base, "prompt.txt"), requested)
		return []byte("hello"), nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, json.Number("9007199254740993"), actualTask.Stimulus.Metadata["large"])
	legacyJSON, err := json.Marshal(legacyTask)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(actualTask)
	require.NoError(t, err)
	require.JSONEq(t, string(legacyJSON), string(actualJSON))
	require.Contains(t, string(actualJSON), "9007199254740993")
}

func TestCapturedOfflineSchemaGuardPrecedesPolymorphicDecode(t *testing.T) {
	data := []byte(`id: task
inputs: {prompt: hello}
graders:
  - name: schema
    type: tool_calls
    config:
      expect:
        - tool: read
          args:
            path:
              json_schema: {$ref: "https://never-fetched.invalid/schema.json"}
`)
	_, err := ParseCapturedTestCaseOffline(data, "task.yaml", func(string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected source read")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "offline")
}

func TestCapturedOfflineStrictSourceErrors(t *testing.T) {
	for _, data := range []string{
		"id: task\ninputs: {prompt: hello}\nunknown: true\n",
		"id: task\nid: other\ninputs: {prompt: hello}\n",
		"id: task\ninputs: {prompt_file: ./prompt.txt}\n",
		"id: task\ninputs: {prompt: hello, context: {large: 18446744073709551616}}\n",
		"id: task\ninputs: {prompt: hello, context: {decimal: 0.1234567890123456789}}\n",
	} {
		_, err := ParseCapturedTestCaseOffline([]byte(data), "task.yaml", func(string) ([]byte, error) {
			return []byte("hello"), nil
		})
		require.Error(t, err)
	}

	_, err := ParseCapturedTestCaseOffline([]byte("id: task\ninputs: {prompt: hello}\n"), "task.yaml", nil)
	require.Error(t, err)
}

func TestCapturedSourceRejectsUnexportedFields(t *testing.T) {
	for _, control := range []struct {
		name        string
		declaration string
		field       string
		eval        bool
	}{
		{"prompt-presence-true", "capturedinlineprompt: true\n", "capturedinlineprompt", false},
		{"prompt-presence-false", "capturedinlineprompt: false\n", "capturedinlineprompt", false},
		{"task-source", "sourcedocument: []\n", "sourcedocument", false},
		{"eval-source", "sourcedocument: []\n", "sourcedocument", true},
	} {
		t.Run(control.name, func(t *testing.T) {
			var err error
			if control.eval {
				_, err = ParseCapturedEvalSpecOffline([]byte(capturedEvalFixture+control.declaration), "eval.yaml")
			} else {
				_, err = ParseCapturedTestCaseOffline([]byte("id: task\ninputs: {prompt: hello}\n"+control.declaration),
					"task.yaml", func(string) ([]byte, error) {
						t.Fatal("invalid source must not read prompt files")
						return nil, fmt.Errorf("unexpected read")
					})
			}
			require.ErrorContains(t, err, fmt.Sprintf("unknown captured native field %q", control.field))
		})
	}
}

func TestCapturedNativeIntegerSemantics(t *testing.T) {
	task, err := ParseCapturedTestCaseOffline([]byte("id: task\ninputs: {prompt: hello, context: {hex: 0xFFFFFFFFFFFFFFFF, nested: [9007199254740993]}}\n"),
		"task.yaml", func(string) ([]byte, error) { return nil, fmt.Errorf("unexpected prompt read") })
	require.NoError(t, err)
	require.Equal(t, json.Number("18446744073709551615"), task.Stimulus.Metadata["hex"])
	require.Equal(t, []any{json.Number("9007199254740993")}, task.Stimulus.Metadata["nested"])
}

func TestCapturedSourceRejectsEmptyNullAndWrongDocumentKinds(t *testing.T) {
	for _, source := range []string{"", "# comment only\n", "\n", "null\n", "~\n", "---\n", "[]\n", "123\n", "true\n", "'scalar'\n"} {
		t.Run(fmt.Sprintf("%q", source), func(t *testing.T) {
			require.NotPanics(t, func() {
				_, err := ParseCapturedEvalSpecOffline([]byte(source), "eval.yaml")
				require.Error(t, err)
			})
			require.NotPanics(t, func() {
				_, err := ParseCapturedTestCaseOffline([]byte(source), "task.yaml", func(string) ([]byte, error) {
					t.Fatal("invalid document must not read a prompt source")
					return nil, fmt.Errorf("unexpected read")
				})
				require.Error(t, err)
			})
		})
	}
}

func TestCapturedSourceTypeAndCoercionGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		eval   bool
	}{
		{"numeric_prompt", "id: task\ninputs: {prompt: 123}\n", false},
		{"boolean_prompt", "id: task\ninputs: {prompt: true}\n", false},
		{"null_prompt", "id: task\ninputs: {prompt: null}\n", false},
		{"numeric_id", "id: 123\ninputs: {prompt: hello}\n", false},
		{"boolean_id", "id: true\ninputs: {prompt: hello}\n", false},
		{"null_id", "id: null\ninputs: {prompt: hello}\n", false},
		{"mapping_prompt", "id: task\ninputs: {prompt: {value: hello}}\n", false},
		{"sequence_prompt", "id: task\ninputs: {prompt: [hello]}\n", false},
		{"scalar_inputs", "id: task\ninputs: hello\n", false},
		{"sequence_inputs", "id: task\ninputs: []\n", false},
		{"null_inputs", "id: task\ninputs: null\n", false},
		{"scalar_context", "id: task\ninputs: {prompt: hello, context: 123}\n", false},
		{"sequence_context", "id: task\ninputs: {prompt: hello, context: []}\n", false},
		{"mapping_files", "id: task\ninputs: {prompt: hello, files: {path: file}}\n", false},
		{"scalar_files", "id: task\ninputs: {prompt: hello, files: file}\n", false},
		{"null_resource_element", "id: task\ninputs: {prompt: hello, files: [null]}\n", false},
		{"numeric_instruction", "id: task\ninputs: {prompt: hello}\ninstruction_files: [123]\n", false},
		{"quoted_bool", "id: task\ninputs: {prompt: hello}\ngolden: 'true'\n", false},
		{"null_bool", "id: task\ninputs: {prompt: hello}\ngolden: null\n", false},
		{"numeric_bool_pointer", "id: task\ninputs: {prompt: hello}\nenabled: 1\n", false},
		{"quoted_integer_pointer", "id: task\ninputs: {prompt: hello}\ntimeout_seconds: '30'\n", false},
		{"null_expectation_struct", "id: task\ninputs: {prompt: hello}\nexpected: null\n", false},
		{"numeric_model", strings.Replace(capturedEvalFixture, "model: offline", "model: 123", 1), true},
		{"boolean_name", strings.Replace(capturedEvalFixture, "name: inert", "name: true", 1), true},
		{"numeric_task_path", strings.Replace(capturedEvalFixture, "[task.yaml]", "[123]", 1), true},
		{"quoted_integer", strings.Replace(capturedEvalFixture, "trials_per_task: 1", "trials_per_task: '1'", 1), true},
		{"sequence_config", strings.Replace(capturedEvalFixture,
			"config: {executor: copilot-sdk, model: offline, trials_per_task: 1, timeout_seconds: 30}", "config: []", 1), true},
		{"null_config", strings.Replace(capturedEvalFixture,
			"config: {executor: copilot-sdk, model: offline, trials_per_task: 1, timeout_seconds: 30}", "config: null", 1), true},
		{"mapping_tasks", strings.Replace(capturedEvalFixture, "tasks: [task.yaml]", "tasks: {path: task.yaml}", 1), true},
		{"null_text_config", strings.Replace(capturedEvalFixture, "{contains: [hello]}", "null", 1), true},
		{"numeric_contains", strings.Replace(capturedEvalFixture, "[hello]", "[123]", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				if tc.eval {
					_, err := ParseCapturedEvalSpecOffline([]byte(tc.source), "eval.yaml")
					require.Error(t, err)
				} else {
					_, err := ParseCapturedTestCaseOffline([]byte(tc.source), "task.yaml", func(string) ([]byte, error) {
						t.Fatal("invalid type must not read a prompt source")
						return nil, fmt.Errorf("unexpected read")
					})
					require.Error(t, err)
				}
			})
		})
	}
}

func TestCapturedSourcePreservesNullableDefaultsAndQuotedStrings(t *testing.T) {
	task, err := ParseCapturedTestCaseOffline([]byte(`id: "123"
inputs:
  prompt: "true"
  context: null
  files: null
  repos: null
  environment: null
  follow_up_prompts: null
  responder: null
enabled: null
timeout_seconds: null
first_event_timeout_seconds: null
instruction_files: null
skill_directories: null
graders: null
checkpoints: null
requirements: null
tags: null
`), "task.yaml", func(string) ([]byte, error) { return nil, fmt.Errorf("unexpected read") })
	require.NoError(t, err)
	require.Equal(t, "123", task.TestID)
	require.Equal(t, "true", task.Stimulus.Message)
	require.Nil(t, task.Active)
	require.Nil(t, task.TimeoutSec)
	require.Nil(t, task.FirstEventTimeoutSec)
	require.Nil(t, task.Stimulus.Metadata)
	require.Nil(t, task.Stimulus.Resources)
	require.Nil(t, task.Stimulus.Responder)
	require.Nil(t, task.InstructionFiles)

	source := strings.Replace(capturedEvalFixture, "timeout_seconds: 30}",
		"timeout_seconds: 30, inject_skill_body: null, instruction_files: null, mcp_servers: null}", 1)
	source += "inputs: null\nmetrics: null\n"
	spec, err := ParseCapturedEvalSpecOffline([]byte(source), "eval.yaml")
	require.NoError(t, err)
	require.Nil(t, spec.Config.InjectSkillBody)
	require.Nil(t, spec.Config.InstructionFiles)
	require.Nil(t, spec.Config.ServerConfigs)
	require.Nil(t, spec.Inputs)
	require.Nil(t, spec.Metrics)
}

func TestCapturedStringGuardsDoNotChangeLegacyLoaderCoercion(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "task.yaml")
	source := []byte("id: 123\ninputs: {prompt: 123}\n")
	require.NoError(t, os.WriteFile(path, source, 0600))
	legacy, err := LoadTestCaseOffline(path)
	require.NoError(t, err)
	require.Equal(t, "123", legacy.TestID)
	require.Equal(t, "123", legacy.Stimulus.Message)
	_, err = ParseCapturedTestCaseOffline(source, path, func(string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected read")
	})
	require.ErrorContains(t, err, "requires !!str")
}

func TestCapturedInlinePromptPresencePreservesSourceAndLegacyLoading(t *testing.T) {
	for _, control := range []struct {
		name    string
		inputs  string
		present bool
		message string
		reads   int
	}{
		{name: "missing-inputs"},
		{name: "empty-inputs", inputs: "inputs: {}\n"},
		{name: "empty-prompt", inputs: "inputs: {prompt: ''}\n", present: true},
		{name: "populated-prompt", inputs: "inputs: {prompt: hello}\n", present: true, message: "hello"},
		{name: "resolved-file", inputs: "inputs: {prompt_file: prompt.txt}\n", message: "hello", reads: 1},
		{name: "empty-file", inputs: "inputs: {prompt_file: ''}\n"},
	} {
		t.Run(control.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "task.yaml")
			source := []byte("id: task\nname: Presence\n" + control.inputs)
			require.NoError(t, os.WriteFile(path, source, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("hello"), 0600))
			legacy, err := LoadTestCaseOffline(path)
			require.NoError(t, err, "legacy admission must remain unchanged")
			require.Equal(t, control.message, legacy.Stimulus.Message)
			reads := 0
			captured, err := ParseCapturedTestCaseOffline(source, path, func(requested string) ([]byte, error) {
				reads++
				require.Equal(t, filepath.Join(dir, "prompt.txt"), requested)
				return []byte("hello"), nil
			})
			require.NoError(t, err)
			require.Equal(t, control.reads, reads)
			require.Equal(t, control.present, captured.HasCapturedInlinePrompt())
			require.Equal(t, control.message, captured.Stimulus.Message)
			require.Equal(t, source, captured.SourceBytes())
			legacyJSON, err := json.Marshal(legacy)
			require.NoError(t, err)
			capturedJSON, err := json.Marshal(captured)
			require.NoError(t, err)
			require.JSONEq(t, string(legacyJSON), string(capturedJSON), "presence is not a new task wire field")
			source[0] = 'X'
			require.Equal(t, control.present, captured.HasCapturedInlinePrompt(), "presence belongs to the captured copy")
			require.Equal(t, byte('i'), captured.SourceBytes()[0])
		})
	}
}
