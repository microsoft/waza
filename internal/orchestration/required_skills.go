package orchestration

import (
	"fmt"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

// ValidateRequiredSkillConfiguration uses runtime declaration discovery without
// initializing an engine. Parse errors are returned, not printed with payloads.
func ValidateRequiredSkillConfiguration(spec *models.EvalSpec, baseDir string) error {
	return validateRequiredSkillConfiguration(spec, baseDir, func(paths []string) (map[string]string, error) {
		return discoverSkillsWithReporter(paths, nil)
	})
}

func validateRequiredSkillConfiguration(spec *models.EvalSpec, baseDir string, discover func([]string) (map[string]string, error)) error {
	if len(spec.Config.RequiredSkills) == 0 || (spec.Scenario == "" && spec.Config.AllSkillsDisabled()) {
		return nil
	}
	if baseDir == "" {
		baseDir = "."
	}
	skillPaths := spec.Config.SkillPaths
	if spec.Scenario != "" {
		skillPaths = spec.Config.FilteredSkillPaths()
	}
	resolvedPaths := utils.ResolvePaths(skillPaths, baseDir)
	if len(resolvedPaths) == 0 {
		if spec.Scenario != "" {
			return fmt.Errorf("required_skills specified but no skill_directories remain enabled; configure a directory not excluded by disabled_skills")
		}
		return fmt.Errorf("required_skills specified but no skill_directories configured")
	}
	discoveredSkills, err := discover(resolvedPaths)
	if err != nil {
		return fmt.Errorf("discovering skills: %w", err)
	}
	if err := validateRequiredSkills(spec.Config.RequiredSkills, discoveredSkills, resolvedPaths); err != nil {
		return fmt.Errorf("skill validation failed:\n%w", err)
	}
	return nil
}
