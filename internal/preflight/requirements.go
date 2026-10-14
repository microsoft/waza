package preflight

import (
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/requirements"
)

func resolveRequirements(r *Report, task *models.TestCase, spec *models.EvalSpec, source string) []RequirementPlan {
	effectiveSpec, unknownIdentity := requirementEvalIdentities(r, spec)
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
			if err := resolveCheck(check, task, effectiveSpec); err != nil {
				if unknownIdentity && check.Scope == "eval" && check.AfterTurn == 0 &&
					strings.TrimSpace(check.Grader) != "" && !hasEvalGraderName(effectiveSpec, check.Grader) {
					if plan.State != Invalid {
						plan.State = Unresolved
						plan.Meaning = "An unexpanded remote declaration could supply this identity; the reference is not verified."
					}
					r.Complete = false
					r.add("requirement.reference_unresolved", Unresolved, source, task.TestID, req.ID,
						"An unnamed remote eval grader has no verified effective identity yet.",
						"Make the pinned preset available offline; requirement metadata does not establish a match or coverage.")
				} else {
					invalid(fmt.Sprintf("requirement %q: %v", req.ID, err))
				}
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

func requirementEvalIdentities(r *Report, spec *models.EvalSpec) (*models.EvalSpec, bool) {
	if spec == nil {
		return nil, false
	}
	effective := *spec
	effective.Graders = append([]models.GraderConfig(nil), spec.Graders...)
	var names []string
	for _, dependency := range r.Dependencies {
		if dependency.Kind == "grader" && dependency.Mode == "locked-cache" {
			names = append(names, dependency.Name)
		}
	}
	unknown, next := false, 0
	for i := range effective.Graders {
		grader := &effective.Graders[i]
		if grader.Ref == "" {
			continue
		}
		// Locked-cache dependencies follow eval reference declaration order,
		// including failed merges whose authored configuration remains intact.
		if next < len(names) && grader.Identifier == "" {
			grader.Identifier = names[next]
		}
		next++
		if grader.Identifier == "" {
			if _, err := registry.ParseRef(grader.Ref); err == nil {
				unknown = true
			}
		}
	}
	return &effective, unknown
}

func hasEvalGraderName(spec *models.EvalSpec, name string) bool {
	if spec != nil {
		for _, grader := range spec.Graders {
			if grader.Identifier == name {
				return true
			}
		}
	}
	return false
}

func resolveCheck(check models.RequirementCheck, task *models.TestCase, spec *models.EvalSpec) error {
	_, err := ResolveGrader(check, task, spec)
	return err
}

// GraderDeclaration has exactly one native declaration on successful lookup:
// Config for eval scope, Inline for task/checkpoint scope. Nothing is normalized.
// It is not a grader instance; callers should treat the borrowed pointers as read-only.
type GraderDeclaration = requirements.GraderDeclaration

// ResolveGrader is a pure scoped declaration lookup. Callers must supply already
// expanded eval graders when a remote preset is needed. It neither loads presets
// nor applies execution defaults, creates graders, or joins runtime results.
func ResolveGrader(check models.RequirementCheck, task *models.TestCase, spec *models.EvalSpec) (GraderDeclaration, error) {
	return requirements.ResolveGrader(check, task, spec)
}
