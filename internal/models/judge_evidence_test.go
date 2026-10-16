package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEvidenceContextNativeAndSchemaAdmission(t *testing.T) {
	type source struct {
		name   string
		schema string
		build  func(string) string
		parse  func([]byte) error
	}
	sources := []source{
		{
			name: "eval", schema: schemas.EvalSchemaJSON,
			build: func(config string) string {
				return "name: evidence\nskill: example\nconfig: {executor: copilot-sdk, model: gpt-5-mini, trials_per_task: 1, timeout_seconds: 60}\ntasks: [tasks/report.yaml]\nmetrics: [{name: pass_rate, weight: 1, threshold: 1}]\ngraders:\n  - type: prompt\n    name: judge\n    config: " + config
			},
			parse: func(data []byte) error { _, err := ParseEvalSpec(data, "eval.yaml"); return err },
		},
		{
			name: "task", schema: schemas.TaskSchemaJSON,
			build: func(config string) string {
				return "id: evidence\nname: Evidence task\ninputs: {prompt: report}\ngraders:\n  - type: prompt\n    name: judge\n    config: " + config
			},
			parse: func(data []byte) error { _, err := ParseTestCase(data, "task.yaml"); return err },
		},
	}
	cases := []struct {
		name, config string
		valid        bool
	}{
		{"omitted", "{prompt: Judge}", true},
		{"empty", "{prompt: Judge, evidence_context: {}}", true},
		{"empty lists", "{prompt: Judge, evidence_context: {files: [], evaluator_only: []}}", true},
		{"selected", "{prompt: Judge, evidence_context: {files: [output/report.json], evaluator_only: [references]}}", true},
		{"unicode", "{prompt: Judge, evidence_context: {files: [résumé.txt]}}", true},
		{"null", "{prompt: Judge, evidence_context: null}", false},
		{"scalar", "{prompt: Judge, evidence_context: false}", false},
		{"array", "{prompt: Judge, evidence_context: []}", false},
		{"null files", "{prompt: Judge, evidence_context: {files: null}}", false},
		{"scalar files", "{prompt: Judge, evidence_context: {files: output.txt}}", false},
		{"number path", "{prompt: Judge, evidence_context: {files: [42]}}", false},
		{"null path", "{prompt: Judge, evidence_context: {files: [null]}}", false},
		{"duplicate path", "{prompt: Judge, evidence_context: {files: [a, a]}}", false},
		{"unknown", "{prompt: Judge, evidence_context: {optional: true}}", false},
		{"resume", "{prompt: Judge, evidence_context: {}, continue_session: true}", false},
		{"pairwise", "{prompt: Judge, evidence_context: {}, mode: pairwise}", false},
		{"independent", "{prompt: Judge, evidence_context: {}, mode: independent, continue_session: false}", true},
		{"traversal", "{prompt: Judge, evidence_context: {files: [../outside]}}", false},
		{"glob", "{prompt: Judge, evidence_context: {files: ['*.txt']}}", false},
		{"empty path", "{prompt: Judge, evidence_context: {files: ['']}}", false},
		{"absolute", "{prompt: Judge, evidence_context: {files: [/outside]}}", false},
		{"windows", "{prompt: Judge, evidence_context: {files: ['C:\\\\outside']}}", false},
	}
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			var schema any
			require.NoError(t, json.Unmarshal([]byte(src.schema), &schema))
			compiler := jsonschema.NewCompiler()
			compiler.UseLoader(schemaloader.Offline{})
			require.NoError(t, compiler.AddResource("schema.json", schema))
			compiled, err := compiler.Compile("schema.json")
			require.NoError(t, err)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					data := []byte(src.build(tc.config))
					var value any
					require.NoError(t, yaml.Unmarshal(data, &value))
					encoded, err := json.Marshal(value)
					require.NoError(t, err)
					schemaErr := compiled.Validate(value)
					require.Equal(t, tc.valid, schemaErr == nil, "schema admission: %v", schemaErr)
					for _, input := range [][]byte{data, encoded} {
						err := src.parse(input)
						require.Equal(t, tc.valid, err == nil, "native admission: %v", err)
					}
				})
			}
		})
	}
}

func TestEvidenceContextRejectsCheckpointAndWrongExecutor(t *testing.T) {
	task := "id: evidence\nname: Evidence task\ninputs: {prompt: report}\ncheckpoints:\n  - after_turn: 1\n    graders:\n      - type: prompt\n        name: judge\n        config: {prompt: Judge, evidence_context: {}}\n"
	_, err := ParseTestCase([]byte(task), "task.yaml")
	require.ErrorContains(t, err, "not checkpoints")
	config := GraderConfig{Identifier: "judge", Kind: GraderKindPrompt, Parameters: PromptGraderParameters{Prompt: "judge", EvidenceContext: &PromptEvidenceContext{}}}
	spec := EvalSpec{Config: Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 60}, Graders: []GraderConfig{config}}
	require.ErrorContains(t, spec.Validate(), "requires executor copilot-sdk")
}

func TestEvidenceContextStrictNewFieldsOnly(t *testing.T) {
	for _, config := range []string{
		`{"prompt":"Judge","evidence_context":{"FILES":[]}}`,
		`{"prompt":"Judge","EVIDENCE_CONTEXT":{}}`,
		`{"prompt":"Judge","evidence_context":{},"evidence_context":null}`,
		`{"prompt":"Judge","evidence_context":{"files":["\ud800"]}}`,
	} {
		var params PromptGraderParameters
		require.Error(t, json.Unmarshal([]byte(config), &params), config)
	}
	var legacy PromptGraderParameters
	require.NoError(t, json.Unmarshal([]byte(`{"prompt":"old","prompt":"new"}`), &legacy))
	require.Equal(t, "new", legacy.Prompt)
	for _, config := range []string{
		"{prompt: Judge, evidence_context: &context {files: &paths [report.txt]}, ignored: *paths}",
		"{prompt: Judge, evidence_context: {files: [" + strings.Repeat("a,", 65) + "]}}",
		"{prompt: Judge, <<: {evidence_context: null}}",
		"{prompt: Judge, <<: {<<: {evidence_context: null}}}",
		"{prompt: Judge, <<: [{evidence_context: null}]}",
		"&configuration {prompt: Judge, evidence_context: {}, ignored: *configuration}",
	} {
		var grader GraderConfig
		require.Error(t, yaml.Unmarshal([]byte("type: prompt\nname: judge\nconfig: "+config), &grader))
	}
	params := PromptGraderParameters{EvidenceContext: &PromptEvidenceContext{Files: make([]string, 65)}}
	require.ErrorContains(t, params.ValidateEvidenceContext(), "64")
	for i := range 64 {
		params.EvidenceContext.Files[i] = fmt.Sprintf("file-%d.txt", i)
	}
	params.EvidenceContext.Files = params.EvidenceContext.Files[:64]
	require.NoError(t, params.ValidateEvidenceContext())
}

func TestJudgeEvidenceExactNumbersAndStrictAdmission(t *testing.T) {
	text := ""
	messages := []JudgeEvidenceMessage{}
	tools := []ToolEvent{{Turn: 1, Sequence: 1, ToolName: "query", Args: map[string]any{"count": json.Number("9007199254740993")}}}
	files := []JudgeEvidenceFile{}
	captured := func() (string, string) { return "captured", "complete" }
	available, completeness := captured()
	input := RunResult{JudgeEvidence: &JudgeEvidence{
		Version: JudgeEvidenceVersion, SHA256: strings.Repeat("0", 64),
		Origin:   EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
		Boundary: "final", ExecutionMode: "mock",
		Task:       JudgeEvidenceSection[string]{Availability: available, Completeness: completeness, Items: &text},
		Output:     JudgeEvidenceSection[string]{Availability: available, Completeness: completeness, Items: &text},
		Messages:   JudgeEvidenceSection[[]JudgeEvidenceMessage]{Availability: available, Completeness: completeness, Items: &messages},
		ToolEvents: JudgeEvidenceSection[[]ToolEvent]{Availability: available, Completeness: completeness, Items: &tools},
		Files:      JudgeEvidenceSection[[]JudgeEvidenceFile]{Availability: available, Completeness: completeness, Items: &files},
		Redaction:  EvidenceRedaction{Policy: "default", Limitations: []string{}},
		Limits:     JudgeEvidenceLimits{MaxFiles: JudgeEvidenceMaxFiles, MaxFileBytes: JudgeEvidenceMaxFileBytes, MaxBundleBytes: JudgeEvidenceMaxBytes},
	}}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	var run RunResult
	require.NoError(t, json.Unmarshal(data, &run))
	require.NotNil(t, run.JudgeEvidence.ToolEvents.Items)
	args, ok := (*run.JudgeEvidence.ToolEvents.Items)[0].Args.(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), args["count"])
	for _, bad := range []string{
		`{"judge_evidence":{"version":"1.0","version":"2.0"}}`,
		`{"judge_evidence":{"Version":"1.0"}}`,
		`{"judge_evidence":{"version":"\ud800"}}`,
		`{"judge_evidence":{"task":{"items":"` + strings.Repeat("a", JudgeEvidenceMaxBytes) + `"}}}`,
	} {
		require.Error(t, json.Unmarshal([]byte(bad), &run))
	}
}
