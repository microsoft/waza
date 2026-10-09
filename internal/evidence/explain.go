package evidence

import (
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/requirements"
)

func PointerSegment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

// ExplainRegradedRequirements leaves original checkpoints in the result but
// never treats them as observations of the newly selected declarations.
func ExplainRegradedRequirements(spec *models.EvalSpec, task *models.TestCase, run *models.RunResult) []models.RequirementExplanation {
	copy := *run
	copy.Checkpoints = nil
	return ExplainRequirements(spec, task, &copy)
}

// ExplainRequirements joins explicit scoped declarations to recorded results.
// It never selects arbitrarily from legacy flattened result-name collisions.
func ExplainRequirements(spec *models.EvalSpec, task *models.TestCase, run *models.RunResult) []models.RequirementExplanation {
	var explanations []models.RequirementExplanation
	for _, requirement := range task.Requirements {
		explanation := models.RequirementExplanation{
			TaskID: task.TestID, RequirementID: requirement.ID, Checks: []models.CheckExplanation{},
		}
		for _, check := range requirement.Checks {
			observation := models.CheckExplanation{
				Check: check, Observation: "unresolved", Category: "insufficient_evidence",
				Message: "The scoped check has no unambiguous recorded result.",
			}
			result, kind, pointer, ok := resolveResult(spec, task, run, check)
			if ok {
				observation.Observation = "grader_recorded_failure"
				if result.Passed {
					observation.Observation = "grader_recorded_pass"
				}
				observation.Category = "grader_observation"
				observation.Message = "The existing grader recorded this result; it is not an enforcement or causal diagnosis."
				if kind == models.GraderKindPrompt {
					observation.Category = "qualitative_observation"
					observation.Message = "The judge recorded this result; it is not a deterministic cause or proof that judge execution completed without errors."
				}
				if kind == models.GraderKindProgram || kind == models.GraderKindInlineScript {
					observation.Category = "operational_status_unavailable"
					observation.Message = "The executable grader recorded this result; typed operational provenance is unavailable, so an agent violation cannot be inferred."
				}
				if kind == models.GraderKindFile || kind == models.GraderKindDiff {
					observation.Category = "insufficient_evidence"
					observation.Message = "The grader recorded a file check, but its actual workspace inputs are not certified by this observation."
				}
				if run.Evidence != nil {
					id := "validations"
					if check.Scope == "checkpoint" {
						id = "checkpoints"
					}
					ref, err := Reference(run.Evidence, id)
					if err == nil {
						ref.Pointer = pointer
						if artifact, err := Resolve(run.Evidence, ref); err == nil && artifact.Availability == "captured" {
							observation.References = []models.EvidenceReference{ref}
						}
					}
				}
			}
			if run.Status == models.StatusError {
				observation.Category = "operational_error"
				observation.Message = "The run recorded an operational error; a check result alone cannot establish requirement satisfaction."
			}
			explanation.Checks = append(explanation.Checks, observation)
		}
		if len(requirement.Checks) == 0 {
			explanation.Checks = append(explanation.Checks, models.CheckExplanation{
				Observation: "unresolved", Category: "insufficient_evidence",
				Message: "No existing check is referenced; descriptive intent is unassessed.",
			})
		}
		explanations = append(explanations, explanation)
	}
	return explanations
}

func resolveResult(spec *models.EvalSpec, task *models.TestCase, run *models.RunResult, check models.RequirementCheck) (models.GraderResults, models.GraderKind, string, bool) {
	var kind models.GraderKind
	declaration, err := requirements.ResolveGrader(check, task, spec)
	if err != nil {
		return models.GraderResults{}, kind, "", false
	}
	if declaration.Config != nil {
		kind = declaration.Config.Kind
	} else {
		kind = declaration.Inline.Kind
	}
	final := 0
	if spec != nil {
		for _, grader := range spec.Graders {
			if grader.Identifier == check.Grader {
				final++
			}
		}
	}
	for _, grader := range task.Validators {
		if grader.Identifier == check.Grader {
			final++
		}
	}
	if check.Scope == "checkpoint" {
		if run.Evidence != nil && run.Evidence.SourceManifestSHA256 != "" {
			return models.GraderResults{}, kind, "", false
		}
		found := -1
		for i, checkpoint := range run.Checkpoints {
			if checkpoint.AfterTurn == check.AfterTurn {
				if found != -1 {
					return models.GraderResults{}, kind, "", false
				}
				found = i
			}
		}
		if found < 0 {
			return models.GraderResults{}, kind, "", false
		}
		result, ok := run.Checkpoints[found].Validations[check.Grader]
		return result, kind, fmt.Sprintf("/%d/validations/%s", found, PointerSegment(check.Grader)), ok
	}
	if check.Grader == "_output_contains" && len(task.Expectation.MustInclude) > 0 ||
		check.Grader == "_output_not_contains" && len(task.Expectation.MustExclude) > 0 ||
		check.Grader == "_output_contains_any" && len(task.Expectation.MayInclude) > 0 {
		final++
	}
	if final != 1 {
		return models.GraderResults{}, kind, "", false
	}
	result, ok := run.Validations[check.Grader]
	return result, kind, "/" + PointerSegment(check.Grader), ok
}
