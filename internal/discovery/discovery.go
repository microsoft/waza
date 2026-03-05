package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoveredSkill represents a skill found during directory traversal.
type DiscoveredSkill struct {
	Name      string // directory name containing SKILL.md
	SkillPath string // absolute path to SKILL.md
	EvalPath  string // absolute path to eval.yaml (empty if not found)
	Dir       string // absolute path to the skill directory
}

// HasEval returns true if the skill has a discovered eval config.
func (d DiscoveredSkill) HasEval() bool {
	return d.EvalPath != ""
}

// Discover walks the given root directory and finds all skills with eval configs.
// A skill is a directory containing SKILL.md. An eval config is eval.yaml either
// in the same directory, in an evals/ subdirectory, or in a tests/ subdirectory.
func Discover(root string) ([]DiscoveredSkill, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}

	// Verify root exists before walking
	if _, err := os.Stat(absRoot); err != nil {
		return nil, fmt.Errorf("root path: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving root symlink: %w", err)
	}

	var skills []DiscoveredSkill
	rootGitHubDir := filepath.Join(resolvedRoot, ".github")
	rootGitHubSkillsDir := filepath.Join(rootGitHubDir, "skills")

	err = filepath.Walk(resolvedRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}

		// Skip hidden directories, except root-level .github
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") {
			if path != rootGitHubDir {
				return filepath.SkipDir
			}
		}

		// Under root .github, only recurse into .github/skills.
		if info.IsDir() &&
			strings.HasPrefix(path, rootGitHubDir+string(filepath.Separator)) &&
			path != rootGitHubSkillsDir &&
			!strings.HasPrefix(path, rootGitHubSkillsDir+string(filepath.Separator)) {
			return filepath.SkipDir
		}

		// Skip node_modules and similar
		if info.IsDir() && (info.Name() == "node_modules" || info.Name() == "vendor") {
			return filepath.SkipDir
		}

		// Look for SKILL.md files
		if !info.IsDir() && info.Name() == "SKILL.md" {
			dir := filepath.Dir(path)
			name := filepath.Base(dir)
			evalPath := findEvalConfig(dir)
			skills = mergeSkillsByName(skills, []DiscoveredSkill{{
				Name:      name,
				SkillPath: path,
				EvalPath:  evalPath,
				Dir:       dir,
			}})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory %s: %w", resolvedRoot, err)
	}

	return skills, nil
}

// findEvalConfig looks for eval.yaml in standard locations relative to a skill directory.
// Priority: tests/eval.yaml > evals/eval.yaml > eval.yaml
func findEvalConfig(skillDir string) string {
	candidates := []string{
		filepath.Join(skillDir, "tests", "eval.yaml"),
		filepath.Join(skillDir, "evals", "eval.yaml"),
		filepath.Join(skillDir, "eval.yaml"),
	}

	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// FilterWithEval returns only skills that have a discovered eval config.
func FilterWithEval(skills []DiscoveredSkill) []DiscoveredSkill {
	var result []DiscoveredSkill
	for _, s := range skills {
		if s.HasEval() {
			result = append(result, s)
		}
	}
	return result
}

// FilterWithoutEval returns only skills that lack an eval config.
func FilterWithoutEval(skills []DiscoveredSkill) []DiscoveredSkill {
	var result []DiscoveredSkill
	for _, s := range skills {
		if !s.HasEval() {
			result = append(result, s)
		}
	}
	return result
}

// fileExists checks if a path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func mergeSkillsByName(base, additional []DiscoveredSkill) []DiscoveredSkill {
	seen := make(map[string]bool, len(base))
	for _, skill := range base {
		seen[skill.Name] = true
	}

	merged := append([]DiscoveredSkill{}, base...)
	for _, skill := range additional {
		if seen[skill.Name] {
			continue
		}
		merged = append(merged, skill)
		seen[skill.Name] = true
	}

	return merged
}
