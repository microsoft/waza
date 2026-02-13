package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/wizard"
)

func newInitCommand() *cobra.Command {
	var interactive bool

	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Initialize a new eval suite",
		Long: `Initialize a new evaluation suite with a compliant directory structure.

Creates an eval.yaml spec file, a tasks/ directory with an example task,
and a fixtures/ directory with an example fixture file.

Use --interactive to run a guided wizard that collects skill metadata
and generates a SKILL.md scaffold.

If no directory is specified, the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return initCommandE(cmd, args, interactive)
		},
	}

	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Run guided skill creation wizard")

	return cmd
}

func initCommandE(cmd *cobra.Command, args []string, interactive bool) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	// Create the root directory if it doesn't exist
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Run interactive wizard if requested
	if interactive {
		spec, err := wizard.RunSkillWizard(cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return fmt.Errorf("wizard failed: %w", err)
		}

		content, err := wizard.GenerateSkillMD(spec)
		if err != nil {
			return fmt.Errorf("failed to generate SKILL.md: %w", err)
		}

		skillPath := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("failed to write SKILL.md: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", skillPath) //nolint:errcheck
	}

	// Create tasks/ and fixtures/ subdirectories
	tasksDir := filepath.Join(dir, "tasks")
	fixturesDir := filepath.Join(dir, "fixtures")

	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		return fmt.Errorf("failed to create tasks directory: %w", err)
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create fixtures directory: %w", err)
	}

	// Generate eval.yaml from BenchmarkSpec
	spec := models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{
			Name:        "my-skill-eval",
			Description: "Evaluation suite for my-skill.",
		},
		SkillName: "my-skill",
		Version:   "1.0",
		Config: models.Config{
			RunsPerTest: 1,
			TimeoutSec:  300,
			Concurrent:  false,
			EngineType:  "mock",
			ModelID:     "gpt-4o",
		},
		Graders: []models.GraderConfig{
			{
				Kind:       "code",
				Identifier: "has_output",
				Parameters: map[string]any{
					"assertions": []string{"len(output) > 0"},
				},
			},
		},
		Tasks: []string{"tasks/*.yaml"},
	}

	specData, err := yaml.Marshal(&spec)
	if err != nil {
		return fmt.Errorf("failed to marshal eval spec: %w", err)
	}

	specPath := filepath.Join(dir, "eval.yaml")
	if err := os.WriteFile(specPath, specData, 0o644); err != nil {
		return fmt.Errorf("failed to write eval.yaml: %w", err)
	}

	// Generate example task
	taskContent := `id: example-task-001
name: Example Task
description: |
  An example task to get you started.
  Replace this with your own test case.

tags:
  - example

inputs:
  prompt: "Explain this code to me"
  files:
    - path: example.py

expected:
  output_contains:
    - "function"
  outcomes:
    - type: task_completed
  behavior:
    max_tool_calls: 5
    max_response_time_ms: 30000
`
	taskPath := filepath.Join(tasksDir, "example-task.yaml")
	if err := os.WriteFile(taskPath, []byte(taskContent), 0o644); err != nil {
		return fmt.Errorf("failed to write example task: %w", err)
	}

	// Generate example fixture
	fixtureContent := `def hello(name):
    """Greet someone by name."""
    return f"Hello, {name}!"
`
	fixturePath := filepath.Join(fixturesDir, "example.py")
	if err := os.WriteFile(fixturePath, []byte(fixtureContent), 0o644); err != nil {
		return fmt.Errorf("failed to write example fixture: %w", err)
	}

	// Print summary
	fmt.Fprintln(cmd.OutOrStdout(), "Initialized eval suite:") //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", specPath)         //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", taskPath)         //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", fixturePath)      //nolint:errcheck

	return nil
}
