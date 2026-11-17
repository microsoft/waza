package models

import (
	"bytes"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

func captureGraderUnknownFields(data []byte, raw any, node *yaml.Node, params GraderParameters) bool {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(raw); err != nil {
		return true
	}
	if node == nil || node.Kind == 0 || params == nil {
		return false
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return true
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(reflect.New(reflect.TypeOf(params)).Interface()) != nil
}

// ValidateWorkspaceCapture rejects no-capture configurations unless every
// grader's evidence needs are known to be independent of workspace files.
func ValidateWorkspaceCapture(spec *EvalSpec, tasks []*TestCase) error {
	if spec.Config.WorkspaceCapture != "" && spec.Config.WorkspaceCapture != "full" && spec.Config.WorkspaceCapture != "none" {
		return fmt.Errorf("workspace_capture must be full or none, got %q", spec.Config.WorkspaceCapture)
	}
	if spec.Config.WorkspaceCapture != "none" {
		return nil
	}
	for _, g := range spec.Graders {
		if g.Ref != "" || g.ScriptPath != "" || g.captureUnknownFields || !captureIndependent(g.Kind, g.Parameters) {
			return fmt.Errorf("workspace_capture none: eval grader %q (type %q) requires or may require workspace evidence", g.Identifier, g.Kind)
		}
	}
	for _, task := range tasks {
		if len(task.Expectation.OutcomeSpecs) != 0 {
			return fmt.Errorf("workspace_capture none: task %q has unsupported outcome expectations", task.TestID)
		}
		for _, g := range task.Validators {
			if g.captureUnknownFields || !captureIndependent(g.Kind, g.Parameters) {
				return fmt.Errorf("workspace_capture none: task %q grader %q (type %q) requires or may require workspace evidence", task.TestID, g.Identifier, g.Kind)
			}
		}
		for _, cp := range task.Checkpoints {
			for _, g := range cp.Graders {
				if g.captureUnknownFields || !captureIndependent(g.Kind, g.Parameters) {
					return fmt.Errorf("workspace_capture none: task %q checkpoint after turn %d grader %q (type %q) requires or may require workspace evidence", task.TestID, cp.AfterTurn, g.Identifier, g.Kind)
				}
			}
		}
	}
	return nil
}

func captureIndependent(kind GraderKind, params GraderParameters) bool {
	switch kind {
	case GraderKindText:
		_, ok := params.(TextGraderParameters)
		return ok
	case GraderKindJSONSchema:
		_, ok := params.(JSONSchemaGraderParameters)
		return ok
	case GraderKindBehavior:
		_, ok := params.(BehaviorGraderParameters)
		return ok
	case GraderKindActionSequence:
		_, ok := params.(ActionSequenceGraderParameters)
		return ok
	case GraderKindSkillInvocation:
		_, ok := params.(SkillInvocationGraderParameters)
		return ok
	case GraderKindToolConstraint:
		_, ok := params.(ToolConstraintGraderParameters)
		return ok
	case GraderKindToolCalls:
		_, ok := params.(ToolCallsGraderParameters)
		return ok
	default:
		return false
	}
}
