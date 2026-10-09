package preflight

import (
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

func resolveRequirements(r *Report, task *models.TestCase, spec *models.EvalSpec, source string) []RequirementPlan {
	plans := make([]RequirementPlan, 0, len(task.Requirements))
	ids := map[string]bool{}
	for _, req := range task.Requirements {
		plan := RequirementPlan{Requirement: req, State: Verified,
			Meaning: "Explicit declarations resolved; descriptive intent only, not enforced or assessed."}
		invalid := func(message string) {
			plan.State = Invalid
			r.add("requirement.invalid", Invalid, source, task.TestID, req.ID, message,
				"Use a unique nonempty ID, a supported category, a description, and unambiguous scoped grader references.")
		}
		if strings.TrimSpace(req.ID) == "" || req.ID != strings.TrimSpace(req.ID) || ids[req.ID] {
			invalid(fmt.Sprintf("requirement ID %q is empty, padded, or duplicated", req.ID))
		}
		ids[req.ID] = true
		switch req.Category {
		case "outcome", "boundary", "recovery", "quality":
		default:
			invalid(fmt.Sprintf("requirement %q has unknown category %q", req.ID, req.Category))
		}
		if strings.TrimSpace(req.Description) == "" {
			invalid(fmt.Sprintf("requirement %q needs a description", req.ID))
		}
		seen := map[models.RequirementCheck]bool{}
		for _, check := range req.Checks {
			if seen[check] {
				invalid(fmt.Sprintf("requirement %q repeats check %+v", req.ID, check))
				continue
			}
			seen[check] = true
			if err := resolveCheck(check, task, spec); err != nil {
				invalid(fmt.Sprintf("requirement %q: %v", req.ID, err))
			}
		}
		if len(req.Checks) == 0 && plan.State != Invalid {
			plan.State = Unresolved
			plan.Meaning = "No existing check is referenced; coverage is not assessed."
			r.add("requirement.uncovered", Unresolved, source, task.TestID, req.ID,
				fmt.Sprintf("requirement %q has no referenced checks", req.ID),
				"Reference an existing observable-state, boundary, recovery, or quality grader; metadata alone is not enforcement.")
		}
		if plan.State == Verified {
			r.add("requirement.references", Verified, source, task.TestID, req.ID,
				plan.Meaning, "Run the selected graders and inspect their evidence; text success is not proof of external resulting state.")
		}
		plans = append(plans, plan)
	}
	return plans
}

func resolveCheck(check models.RequirementCheck, task *models.TestCase, spec *models.EvalSpec) error {
	if strings.TrimSpace(check.Grader) == "" {
		return fmt.Errorf("grader reference is empty")
	}
	if check.Scope != "checkpoint" && check.AfterTurn != 0 {
		return fmt.Errorf("after_turn is only valid for checkpoint scope")
	}
	count := 0
	switch check.Scope {
	case "eval":
		for _, grader := range spec.Graders {
			if grader.Identifier == check.Grader {
				count++
			}
		}
	case "task":
		for _, grader := range task.Validators {
			if grader.Identifier == check.Grader {
				count++
			}
		}
	case "checkpoint":
		if check.AfterTurn < 1 {
			return fmt.Errorf("checkpoint reference requires after_turn >= 1")
		}
		for _, checkpoint := range task.Checkpoints {
			if checkpoint.AfterTurn == check.AfterTurn {
				for _, grader := range checkpoint.Graders {
					if grader.Identifier == check.Grader {
						count++
					}
				}
			}
		}
	default:
		return fmt.Errorf("unknown scope %q; use eval, task, or checkpoint", check.Scope)
	}
	if count != 1 {
		return fmt.Errorf("%s grader %q at turn %d resolves to %d declarations; expected exactly one", check.Scope, check.Grader, check.AfterTurn, count)
	}
	return nil
}
