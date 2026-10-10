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
	if spec.Config.AllSkillsDisabled() || len(spec.Config.RequiredSkills) == 0 {
		return nil
	}
	if baseDir == "" {
		baseDir = "."
	}
	resolvedPaths := utils.ResolvePaths(spec.Config.SkillPaths, baseDir)
	if len(resolvedPaths) == 0 {
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
