package orchestration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/skill"
)

// discoverSkills scans the provided directories for SKILL.md and .agent.md
// files and returns a map of skill/agent names to their file paths.
// SKILL.md takes priority over .agent.md when both exist in the same directory.
func discoverSkills(directories []string) (map[string]string, error) {
	return discoverSkillsWithReporter(directories, func(path string, err error) {
		fmt.Fprintf(os.Stderr, "warning: failed to parse %s: %v\n", path, err)
	})
}

func discoverSkillsWithReporter(directories []string, warn func(string, error)) (map[string]string, error) {
	discovered := make(map[string]string)

	for _, dir := range directories {
		// Check if directory exists
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		// Look for SKILL.md first (takes priority)
		skillPath := filepath.Join(dir, "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			skillName, err := parseSkillName(skillPath)
			if err != nil {
				if warn == nil {
					return nil, fmt.Errorf("parsing skill definition: %w", err)
				}
				warn(skillPath, err)
				continue
			}
			if skillName != "" {
				discovered[skillName] = skillPath
			}
			continue
		}

		// Look for .agent.md files
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && skill.IsAgentFile(entry.Name()) {
				agentPath := filepath.Join(dir, entry.Name())
				agentName, err := parseAgentName(agentPath)
				if err != nil {
					if warn == nil {
						return nil, fmt.Errorf("parsing agent definition: %w", err)
					}
					warn(agentPath, err)
					continue
				}
				if agentName != "" {
					discovered[agentName] = agentPath
				}
				break // only one agent per directory
			}
		}
	}

	return discovered, nil
}

// parseSkillName reads a SKILL.md file and extracts the skill name from frontmatter.
func parseSkillName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	var s skill.Skill
	if err := s.UnmarshalText(data); err != nil {
		return "", fmt.Errorf("parsing SKILL.md: %w", err)
	}

	return strings.TrimSpace(s.Frontmatter.Name), nil
}

// parseAgentName reads an .agent.md file and extracts the agent name from frontmatter.
func parseAgentName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	fm, _, err := skill.ParseAgentFrontmatter(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing agent frontmatter: %w", err)
	}

	name := strings.TrimSpace(fm.Name)
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, ".agent.md")
	}
	return name, nil
}

// validateRequiredSkills checks that all required skills are discovered.
// Returns nil if validation passes, or an error describing what's missing.
func validateRequiredSkills(requiredSkills []string, discoveredSkills map[string]string, searchedDirs []string) error {
	if len(requiredSkills) == 0 {
		// No required skills specified, validation passes
		return nil
	}

	var missing []string
	for _, required := range requiredSkills {
		if _, found := discoveredSkills[required]; !found {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		// Build error message
		var sb strings.Builder
		sb.WriteString("required skills not found:\n")
		for _, skillName := range missing {
			fmt.Fprintf(&sb, "  - %s\n", skillName)
		}
		sb.WriteString("\nSearched directories:\n")
		for _, dir := range searchedDirs {
			fmt.Fprintf(&sb, "  - %s\n", dir)
		}
		if len(discoveredSkills) > 0 {
			sb.WriteString("\nFound skills:\n")
			for skillName := range discoveredSkills {
				fmt.Fprintf(&sb, "  - %s\n", skillName)
			}
		} else {
			sb.WriteString("\nNo skills were found in the searched directories.\n")
		}
		return errors.New(sb.String())
	}

	return nil
}
