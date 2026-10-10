// Package requirements resolves descriptive metadata against native declarations.
package requirements

import (
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// GraderDeclaration has exactly one native declaration on successful lookup:
// Config for eval scope, Inline for task/checkpoint scope. Nothing is normalized.
// It is not a grader instance; callers should treat the borrowed pointers as read-only.
type GraderDeclaration struct {
	Config *models.GraderConfig
	Inline *models.ValidatorInline
}

// ResolveGrader is a pure scoped declaration lookup. Callers must supply already
// expanded eval graders when a remote preset is needed. It neither loads presets
// nor applies execution defaults, creates graders, or joins runtime results.
func ResolveGrader(check models.RequirementCheck, task *models.TestCase, spec *models.EvalSpec) (GraderDeclaration, error) {
	var declaration GraderDeclaration
	if strings.TrimSpace(check.Grader) == "" {
		return declaration, fmt.Errorf("grader reference is empty")
	}
	if check.Scope != "checkpoint" && check.AfterTurn != 0 {
		return declaration, fmt.Errorf("after_turn is only valid for checkpoint scope")
	}
	count := 0
	inline := func(grader *models.ValidatorInline) {
		if grader.Identifier == check.Grader {
			count++
			declaration = GraderDeclaration{
				Inline: grader,
			}
		}
	}
	switch check.Scope {
	case "eval":
		if spec == nil {
			return declaration, fmt.Errorf("eval scope requires an eval specification")
		}
		for i := range spec.Graders {
			grader := &spec.Graders[i]
			if grader.Identifier == check.Grader {
				count++
				declaration = GraderDeclaration{Config: grader}
			}
		}
	case "task":
		if task == nil {
			return declaration, fmt.Errorf("task scope requires a task declaration")
		}
		for i := range task.Validators {
			inline(&task.Validators[i])
		}
	case "checkpoint":
		if check.AfterTurn < 1 {
			return declaration, fmt.Errorf("checkpoint reference requires after_turn >= 1")
		}
		if task == nil {
			return declaration, fmt.Errorf("checkpoint scope requires a task declaration")
		}
		for i := range task.Checkpoints {
			checkpoint := &task.Checkpoints[i]
			if checkpoint.AfterTurn == check.AfterTurn {
				for j := range checkpoint.Graders {
					inline(&checkpoint.Graders[j])
				}
			}
		}
	default:
		return declaration, fmt.Errorf("unknown scope %q; use eval, task, or checkpoint", check.Scope)
	}
	if count != 1 {
		return GraderDeclaration{}, fmt.Errorf("%s grader %q at turn %d resolves to %d declarations; expected exactly one", check.Scope, check.Grader, check.AfterTurn, count)
	}
	return declaration, nil
}
