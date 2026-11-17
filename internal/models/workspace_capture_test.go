package models

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestValidateWorkspaceCapture(t *testing.T) {
	text := GraderConfig{Identifier: "text", Kind: GraderKindText, Parameters: TextGraderParameters{Contains: []string{"ok"}}}
	task := &TestCase{TestID: "task"}
	spec := &EvalSpec{Config: Config{WorkspaceCapture: "none"}, Graders: []GraderConfig{text}}
	require.NoError(t, ValidateWorkspaceCapture(spec, []*TestCase{task}))
	for _, kind := range []GraderKind{GraderKindFile, GraderKindDiff, GraderKindInlineScript, GraderKindProgram, GraderKindPrompt, "unknown"} {
		t.Run(string(kind), func(t *testing.T) {
			bad := GraderConfig{Identifier: "bad", Kind: kind, Parameters: GenericGraderParameters{}}
			spec.Graders = []GraderConfig{bad}
			require.ErrorContains(t, ValidateWorkspaceCapture(spec, []*TestCase{task}), "eval grader")
			spec.Graders = []GraderConfig{text}
			v := ValidatorInline{Identifier: "bad", Kind: kind, Parameters: bad.Parameters}
			task.Validators = []ValidatorInline{v}
			require.ErrorContains(t, ValidateWorkspaceCapture(spec, []*TestCase{task}), "task")
			task.Validators = nil
			task.Checkpoints = []Checkpoint{{AfterTurn: 1, Graders: []ValidatorInline{v}}}
			require.ErrorContains(t, ValidateWorkspaceCapture(spec, []*TestCase{task}), "checkpoint")
			task.Checkpoints = nil
		})
	}

	spec.Graders = []GraderConfig{{Identifier: "judge", Kind: GraderKindPrompt, Parameters: PromptGraderParameters{EvidenceContext: &PromptEvidenceContext{Files: []string{"report"}}}}}
	require.Error(t, ValidateWorkspaceCapture(spec, []*TestCase{task}))
	spec.Config.WorkspaceCapture = "invalid"
	require.ErrorContains(t, ValidateWorkspaceCapture(spec, nil), "must be full or none")
}

func TestNoCaptureRejectsUnknownGraderConfiguration(t *testing.T) {
	for _, field := range []string{"future_evidence: true", "config:\n  contains: [ok]\n  future_evidence: true"} {
		t.Run(field, func(t *testing.T) {
			source := "name: check\ntype: text\n" + field + "\n"
			var grader ValidatorInline
			require.NoError(t, yaml.Unmarshal([]byte(source), &grader))
			task := &TestCase{TestID: "test", Validators: []ValidatorInline{grader}}
			spec := &EvalSpec{Config: Config{WorkspaceCapture: "none"}}
			require.ErrorContains(t, ValidateWorkspaceCapture(spec, []*TestCase{task}), "workspace_capture none")
			spec.Config.WorkspaceCapture = "full"
			require.NoError(t, ValidateWorkspaceCapture(spec, []*TestCase{task}))
		})
	}
}
