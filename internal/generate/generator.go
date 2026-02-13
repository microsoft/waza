package generate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillFrontmatter holds parsed SKILL.md YAML frontmatter fields.
type SkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseSkillMD reads a SKILL.md file and extracts the YAML frontmatter.
func ParseSkillMD(path string) (fm *SkillFrontmatter, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening skill file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// First line must be "---"
	if !scanner.Scan() {
		return nil, fmt.Errorf("skill file is empty")
	}
	if strings.TrimSpace(scanner.Text()) != "---" {
		return nil, fmt.Errorf("skill file missing YAML frontmatter delimiter (---)")
	}

	// Collect lines until closing "---"
	var lines []string
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			found = true
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading skill file: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("skill file missing closing frontmatter delimiter (---)")
	}

	raw := strings.Join(lines, "\n")
	var parsed SkillFrontmatter
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parsing frontmatter YAML: %w", err)
	}

	if parsed.Name == "" {
		return nil, fmt.Errorf("skill frontmatter missing required 'name' field")
	}

	if err := sanitizeSkillName(parsed.Name); err != nil {
		return nil, err
	}

	return &parsed, nil
}

// sanitizeSkillName rejects names that could cause path traversal or are empty.
func sanitizeSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name must not be empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("skill name %q contains invalid path characters", name)
	}
	return nil
}

// GenerateEvalSuite creates an eval.yaml, task files, and a fixtures directory
// inside outputDir based on the parsed skill frontmatter.
func GenerateEvalSuite(skill *SkillFrontmatter, outputDir string) error {
	tasksDir := filepath.Join(outputDir, "tasks")
	fixturesDir := filepath.Join(outputDir, "fixtures")

	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		return fmt.Errorf("creating tasks directory: %w", err)
	}
	if err := os.MkdirAll(fixturesDir, 0755); err != nil {
		return fmt.Errorf("creating fixtures directory: %w", err)
	}

	// Generate a starter task file.
	// NOTE: This produces base scaffolding with placeholder tasks. Trigger
	// parsing from the SKILL.md body will be enhanced in a future iteration.
	taskFile := filepath.Join(tasksDir, fmt.Sprintf("%s-basic.yaml", skill.Name))
	if err := writeTaskFile(taskFile, skill); err != nil {
		return fmt.Errorf("writing task file: %w", err)
	}

	// Generate a placeholder fixture
	fixturePath := filepath.Join(fixturesDir, "sample.txt")
	if err := writeFixtureFile(fixturePath, skill); err != nil {
		return fmt.Errorf("writing fixture file: %w", err)
	}

	// Generate eval.yaml
	evalPath := filepath.Join(outputDir, "eval.yaml")
	if err := writeEvalFile(evalPath, skill); err != nil {
		return fmt.Errorf("writing eval.yaml: %w", err)
	}

	return nil
}

func writeTaskFile(path string, skill *SkillFrontmatter) error {
	task := map[string]any{
		"id":          fmt.Sprintf("%s-basic-001", skill.Name),
		"name":        fmt.Sprintf("Basic %s test", skill.Name),
		"description": fmt.Sprintf("A starter test case for the %s skill.", skill.Name),
		"tags":        []string{"generated", "basic"},
		"inputs": map[string]any{
			"prompt": fmt.Sprintf("Use the %s skill on the provided input.", skill.Name),
			"files":  []map[string]string{{"path": "sample.txt"}},
		},
		"expected": map[string]any{
			"output_contains": []string{},
			"outcomes":        []map[string]string{{"type": "task_completed"}},
			"behavior": map[string]int{
				"max_tool_calls": 10,
			},
		},
		"graders": []map[string]any{
			{
				"name": "has_output",
				"type": "code",
				"config": map[string]any{
					"assertions": []string{"len(output) > 0"},
				},
			},
		},
	}

	return writeYAML(path, task)
}

func writeFixtureFile(path string, skill *SkillFrontmatter) error {
	content := fmt.Sprintf("# Sample fixture for %s\nReplace this with real test data.\n", skill.Name)
	return os.WriteFile(path, []byte(content), 0644)
}

func writeEvalFile(path string, skill *SkillFrontmatter) error {
	desc := skill.Description
	if desc == "" {
		desc = fmt.Sprintf("Evaluation suite for the %s skill.", skill.Name)
	}

	eval := map[string]any{
		"name":        fmt.Sprintf("%s-eval", skill.Name),
		"description": desc,
		"skill":       skill.Name,
		"version":     "1.0",
		"config": map[string]any{
			"trials_per_task": 3,
			"timeout_seconds": 300,
			"parallel":        false,
			"executor":        "mock",
			"model":           "claude-sonnet-4-20250514",
		},
		"metrics": []map[string]any{
			{
				"name":        "task_completion",
				"weight":      0.5,
				"threshold":   0.8,
				"description": "Did the skill complete the task?",
			},
			{
				"name":        "behavior_quality",
				"weight":      0.5,
				"threshold":   0.7,
				"description": "Is the skill behavior appropriate?",
			},
		},
		"graders": []map[string]any{
			{
				"type": "code",
				"name": "has_output",
				"config": map[string]any{
					"assertions": []string{"len(output) > 10"},
				},
			},
		},
		"tasks": []string{"tasks/*.yaml"},
	}

	return writeYAML(path, eval)
}

func writeYAML(path string, data any) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling YAML: %w", err)
	}
	return os.WriteFile(path, out, 0644)
}
