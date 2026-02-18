package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/spboyer/waza/internal/generate"
	"github.com/spboyer/waza/internal/scaffold"
	"github.com/spboyer/waza/internal/wizard"
)

func newNewCommand() *cobra.Command {
	var template string

	cmd := &cobra.Command{
		Use:   "new <skill-name>",
		Short: "Create a new skill with its eval suite",
		Long: `Create a new skill and its evaluation suite with a compliant directory structure.

Idempotent: detects what already exists and fills in only the missing pieces.
If SKILL.md already exists, it is parsed for the skill name instead of being
regenerated.

Two modes of operation:

  Inside a project (skills/ directory detected):
    Creates skills/{name}/SKILL.md and evals/{name}/ with eval.yaml,
    task files, and fixtures.

  Standalone (no skills/ directory):
    Creates {name}/ with SKILL.md, evals/, .github/workflows/eval.yml,
    .gitignore, and README.md.

When running in a terminal (TTY), launches an interactive wizard for skill
metadata collection. In non-interactive environments (CI, pipes), uses defaults.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newCommandE(cmd, args, template)
		},
	}

	cmd.Flags().StringVarP(&template, "template", "t", "", "Template pack to use (coming soon)")

	return cmd
}

func newCommandE(cmd *cobra.Command, args []string, templatePack string) error {
	skillName := args[0]

	if err := scaffold.ValidateName(skillName); err != nil {
		return err
	}

	if templatePack != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Note: template packs coming soon. Using default template.") //nolint:errcheck
	}

	// Determine mode based on skills/ directory presence
	projectRoot, inProject := findProjectRoot()

	// Check if SKILL.md already exists — if so, skip wizard/generation
	skillMDContent, skillMDExists := detectExistingSkillMD(projectRoot, inProject, skillName)

	if !skillMDExists {
		// Check TTY from the command's input stream, not os.Stdin directly.
		inReader := cmd.InOrStdin()
		isTTY := false
		if f, ok := inReader.(*os.File); ok {
			isTTY = term.IsTerminal(int(f.Fd()))
		}
		if isTTY {
			spec, err := wizard.RunSkillWizard(cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("wizard failed: %w", err)
			}
			if spec.Name != "" && spec.Name != skillName {
				return fmt.Errorf("wizard name %q does not match CLI argument %q", spec.Name, skillName)
			}
			spec.Name = skillName
			content, err := wizard.GenerateSkillMD(spec)
			if err != nil {
				return fmt.Errorf("failed to generate SKILL.md: %w", err)
			}
			skillMDContent = content
		} else {
			skillMDContent = defaultSkillMD(skillName)
		}
	}

	if inProject {
		return scaffoldInProject(cmd, projectRoot, skillName, skillMDContent)
	}
	return scaffoldStandalone(cmd, skillName, skillMDContent)
}

// detectExistingSkillMD checks whether a SKILL.md already exists for the given
// skill name and returns its content if so. Returns ("", false) if not found.
func detectExistingSkillMD(projectRoot string, inProject bool, skillName string) (string, bool) {
	var skillMDPath string
	if inProject {
		skillMDPath = filepath.Join(projectRoot, "skills", skillName, "SKILL.md")
	} else {
		skillMDPath = filepath.Join(skillName, "SKILL.md")
	}

	if _, err := os.Stat(skillMDPath); err == nil {
		// Try to parse for validation; either way keep existing content
		generate.ParseSkillMD(skillMDPath) //nolint:errcheck // validation only; we keep existing content regardless
		data, readErr := os.ReadFile(skillMDPath)
		if readErr == nil {
			return string(data), true
		}
	}
	return "", false
}

// findProjectRoot walks up from CWD looking for a skills/ directory.
// Returns the directory containing skills/ and true, or ("", false) if not found.
func findProjectRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}

	for {
		candidate := filepath.Join(dir, "skills")
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return dir, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", false
}

// scaffoldInProject creates files within an existing project structure.
func scaffoldInProject(cmd *cobra.Command, projectRoot, skillName, skillMD string) error {
	engine, model := scaffold.ReadProjectDefaults()
	skillDir := filepath.Join(projectRoot, "skills", skillName)
	evalDir := filepath.Join(projectRoot, "evals", skillName)
	tasksDir := filepath.Join(evalDir, "tasks")
	fixturesDir := filepath.Join(evalDir, "fixtures")

	// Create directories
	for _, d := range []string{skillDir, tasksDir, fixturesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	tasks := scaffold.TaskFiles(skillName)
	files := []fileEntry{
		{filepath.Join(skillDir, "SKILL.md"), skillMD},
		{filepath.Join(evalDir, "eval.yaml"), scaffold.EvalYAML(skillName, engine, model)},
	}
	for name, content := range tasks {
		files = append(files, fileEntry{filepath.Join(tasksDir, name), content})
	}
	files = append(files, fileEntry{filepath.Join(fixturesDir, "sample.py"), scaffold.Fixture()})

	return writeFiles(cmd, files)
}

// scaffoldStandalone creates a self-contained skill directory.
func scaffoldStandalone(cmd *cobra.Command, skillName, skillMD string) error {
	engine, model := scaffold.ReadProjectDefaults()
	rootDir := skillName
	evalsDir := filepath.Join(rootDir, "evals")
	tasksDir := filepath.Join(evalsDir, "tasks")
	fixturesDir := filepath.Join(evalsDir, "fixtures")
	workflowDir := filepath.Join(rootDir, ".github", "workflows")

	// Create directories
	for _, d := range []string{tasksDir, fixturesDir, workflowDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	tasks := scaffold.TaskFiles(skillName)
	files := []fileEntry{
		{filepath.Join(rootDir, "SKILL.md"), skillMD},
		{filepath.Join(evalsDir, "eval.yaml"), scaffold.EvalYAML(skillName, engine, model)},
	}
	for name, content := range tasks {
		files = append(files, fileEntry{filepath.Join(tasksDir, name), content})
	}
	files = append(files, fileEntry{filepath.Join(fixturesDir, "sample.py"), scaffold.Fixture()})
	files = append(files,
		fileEntry{filepath.Join(workflowDir, "eval.yml"), defaultCIWorkflow(skillName)},
		fileEntry{filepath.Join(rootDir, ".gitignore"), defaultGitignore()},
		fileEntry{filepath.Join(rootDir, "README.md"), defaultReadme(skillName)},
	)

	return writeFiles(cmd, files)
}

// fileEntry pairs a path with its content for batch writing.
type fileEntry struct {
	path    string
	content string
}

// writeFiles writes each file, skipping any that already exist.
func writeFiles(cmd *cobra.Command, files []fileEntry) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Scaffolding skill:") //nolint:errcheck

	for _, f := range files {
		if _, err := os.Stat(f.path); err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  skip %s (already exists)\n", f.path) //nolint:errcheck
			continue
		}

		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", f.path, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  create %s\n", f.path) //nolint:errcheck
	}

	return nil
}

// --- Template content functions (standalone-only templates remain here) ---

func defaultSkillMD(name string) string {
	return fmt.Sprintf(`---
name: %s
type: utility
description: |
  USE FOR: %s tasks, ...
  DO NOT USE FOR: unrelated tasks, ...
---

# %s

## Overview

Describe what this skill does and when an agent should use it.

## Usage

Provide examples of prompts that should trigger this skill.

## References

List any reference documents or APIs this skill depends on.
`, name, name, scaffold.TitleCase(name))
}

func defaultCIWorkflow(name string) string {
	return fmt.Sprintf(`name: Eval %s

on:
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  run-eval:
    name: Run Evaluation
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install waza
        run: |
          curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash

      - name: Run eval
        run: |
          waza run evals/eval.yaml --context-dir evals/fixtures -v
`, scaffold.TitleCase(name))
}

func defaultGitignore() string {
	return `results.json
.waza-cache/
coverage.txt
`
}

func defaultReadme(name string) string {
	return fmt.Sprintf(`# %s

A skill for agent evaluation with [waza](https://github.com/spboyer/waza).

## Quick Start

1. Edit `+"`SKILL.md`"+` with your skill's description and triggers.
2. Customize the task files in `+"`evals/tasks/`"+`.
3. Add real fixtures to `+"`evals/fixtures/`"+`.
4. Run the evaluation:

`+"```bash"+`
waza run evals/eval.yaml --context-dir evals/fixtures -v
`+"```"+`

## Structure

`+"```"+`
%s/
├── SKILL.md                  # Skill definition
├── evals/
│   ├── eval.yaml             # Eval configuration
│   ├── tasks/
│   │   ├── basic-usage.yaml
│   │   ├── edge-case.yaml
│   │   └── should-not-trigger.yaml
│   └── fixtures/
│       └── sample.py
├── .github/workflows/
│   └── eval.yml              # CI workflow
├── .gitignore
└── README.md
`+"```"+`

## Learn More

- [Waza Documentation](https://github.com/spboyer/waza)
`, scaffold.TitleCase(name), name)
}
