package preflight

import (
	"os"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

func inspectBoundaryPolicy(r *Report, task *models.TestCase, spec *models.EvalSpec, source string, paths []string) {
	for _, requirement := range task.Requirements {
		if requirement.Category == "boundary" {
			r.add("boundary.post_run", Unresolved, source, task.TestID, requirement.ID,
				"Referenced boundary graders observe recorded evidence after execution; they are not preventive runtime controls.",
				"Review requested runtime policy separately and inspect actual tool/side-effect evidence; static references do not prove enforcement.")
		}
	}
	if spec.SkillsDisabledForTask(task.SkillPaths) || spec.SkillName == "" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		r.add("boundary.discovery", Unresolved, source, task.TestID, "", "Effective agent discovery base is unavailable.", "Use an accessible working directory before evaluating requested boundary intent.")
		r.Complete = false
		return
	}
	agentPath, frontmatter, err := execution.ResolveAgentDefinition(append([]string{cwd}, paths...), spec.SkillName)
	if err != nil {
		r.add("boundary.agent", Invalid, source, task.TestID, "", "Selected agent definition cannot be parsed.", "Correct the selected .agent.md frontmatter; discovery follows runtime precedence.")
		return
	}
	if frontmatter == nil || frontmatter.Tools == nil {
		return
	}
	mode := "requested-allowlist"
	if len(*frontmatter.Tools) == 0 {
		mode = "requested-deny-all"
	}
	r.Dependencies = append(r.Dependencies, Dependency{Kind: "boundary", Name: agentPath, Mode: mode, State: Unresolved, TaskID: task.TestID})
	r.add("boundary.requested_policy", Unresolved, source, task.TestID, "",
		"Selected agent tools frontmatter declares boundary intent, not verified runtime enforcement or observed tool behavior.",
		"Inspect actual runtime policy/denial and resulting-state evidence; preflight never initializes an engine to test the policy.")
}
